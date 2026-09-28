package tui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/chatgpt"
	"larik/internal/llm/ollama"
	"larik/internal/providers"
)

// providerManager is the /providers screen: each connected or detected
// provider with its status, and keys to edit, test, remove or add one.
type providerManager struct {
	list    picker
	testing map[string]bool
	confirm string // provider waiting for a second d to be removed
	note    string // result of the last action
	noteErr bool
}

type providerTestedMsg struct {
	name string
	res  providerModels
}

func (m *model) openProviders() tea.Cmd {
	m.provs = &providerManager{testing: map[string]bool{}}
	m.buildProviders()
	return m.loadModels()
}

// providerNames lists the rows: usable providers plus the one in use.
func (m *model) providerNames() []string {
	names := providers.Usable(m.opts.Config)
	if cur := m.agent.ProviderName(); !slices.Contains(names, cur) {
		names = append(names, cur)
	}
	return names
}

// providerStatus describes a provider's state from its last model list.
func (m *model) providerStatus(name string) (status string, ok, warn bool) {
	cfg := m.opts.Config
	c, builtin := providers.ChoiceFor(name)
	if m.provs != nil && m.provs.testing[name] {
		return "checking…", false, false
	}
	if builtin && c.SignIn && providers.EndpointFor(cfg, name).Token == nil {
		return "needs sign-in", false, true
	}
	if builtin && !c.Local && !c.SignIn && providers.EndpointFor(cfg, name).Key == "" {
		return "needs a key", false, true
	}
	res, loaded := m.modelLists[name]
	switch {
	case !loaded:
		return "checking…", false, false
	case res.err == nil:
		n := 0
		for _, md := range res.models {
			if md.Chat {
				n++
			}
		}
		return fmt.Sprintf("● connected · %d models", n), true, false
	case strings.Contains(res.err.Error(), "rejected"):
		return "✗ key rejected", false, true
	case strings.Contains(res.err.Error(), "can't reach") && builtin && c.Local:
		return "○ not running", false, false
	}
	return "✗ unreachable", false, true
}

func (m *model) buildProviders() {
	pm := m.provs
	cfg := m.opts.Config
	defProvider, _, _ := strings.Cut(cfg.Model, "/")
	var items []pickItem
	names := m.providerNames()
	for _, n := range names {
		status, ok, warn := m.providerStatus(n)
		var tags []string
		if n == defProvider {
			tags = append(tags, "default")
		}
		if n == m.agent.ProviderName() {
			tags = append(tags, "in use")
		}
		label := n
		if len(tags) > 0 {
			label += " (" + strings.Join(tags, ", ") + ")"
		}
		items = append(items, pickItem{label: label, detail: endpointSummary(providers.EndpointFor(cfg, n)), note: status, noteOK: ok, noteWarn: warn, value: pickProvider{n}})
	}
	for _, name := range []string{"nvidia-nim"} {
		if slices.Contains(names, name) {
			continue
		}
		c, _ := providers.ChoiceFor(name)
		items = append(items, pickItem{label: "+ Connect " + c.Title + "…", detail: c.Desc, value: pickConnect{name}})
	}
	items = append(items, pickItem{label: "+ Add provider…", detail: "setup wizard: cloud, local or custom", value: pickAdd{}})
	prev, had := pm.list.selected()
	pm.list.items = items
	if had {
		pm.list.selectWhere(func(it pickItem) bool { return it.value == prev.value })
	} else {
		pm.list.home()
	}
}

type pickProvider struct{ name string }

func endpointSummary(e providers.Endpoint) string {
	if c, ok := providers.ChoiceFor(e.Kind); ok && !c.Local && e.BaseURL == c.BaseURL {
		return c.Title
	}
	return hostOf(e.BaseURL)
}

func (m *model) handleProvidersKey(msg tea.KeyPressMsg) tea.Cmd {
	pm := m.provs
	k := msg.String()
	confirm := pm.confirm
	pm.confirm = ""
	it, has := pm.list.selected()
	sel, isProvider := it.value.(pickProvider)
	switch k {
	case "esc", "ctrl+c", "q":
		m.provs = nil
		return nil
	case "a":
		return m.openWizardFromProviders("")
	case "t":
		if isProvider {
			return m.testProvider(sel.name)
		}
		return nil
	case "d":
		if !isProvider {
			return nil
		}
		return m.removeProvider(sel.name, confirm == sel.name)
	case "enter", "e":
		if !has {
			return nil
		}
		if _, add := it.value.(pickAdd); add {
			return m.openWizardFromProviders("")
		}
		if connect, ok := it.value.(pickConnect); ok {
			return m.openWizardFromProviders(connect.provider)
		}
		return m.openWizardFromProviders(sel.name)
	}
	pm.list.handleKey(msg)
	return nil
}

func (m *model) testProvider(name string) tea.Cmd {
	m.provs.testing[name] = true
	m.provs.note = ""
	m.buildProviders()
	ep := providers.EndpointFor(m.opts.Config, name)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ms, err := ep.ListModels(ctx)
		return providerTestedMsg{name, providerModels{ms, err}}
	}
}

// removeProvider asks for confirmation, then deletes the provider from
// the personal settings files.
func (m *model) removeProvider(name string, confirmed bool) tea.Cmd {
	pm, cfg := m.provs, m.opts.Config
	switch {
	case name == m.agent.ProviderName():
		pm.note, pm.noteErr = name+" is in use; switch model first (alt+p)", true
		return nil
	case name == providers.Codex:
		path := chatgpt.Path(cfg.ConfigDir)
		if !chatgpt.SignedIn(path) {
			pm.note, pm.noteErr = "not signed in to ChatGPT", true
			return nil
		}
		if !confirmed {
			pm.confirm = name
			pm.note, pm.noteErr = "press d again to sign out of ChatGPT", true
			return nil
		}
		if err := os.Remove(path); err != nil {
			pm.note, pm.noteErr = err.Error(), true
			return nil
		}
		pm.note, pm.noteErr = "signed out of ChatGPT", false
		delete(m.modelLists, name)
		m.buildProviders()
		return nil
	}
	if _, saved := cfg.Providers[name]; !saved {
		pm.note, pm.noteErr = "nothing saved for "+name, true
		if c, ok := providers.ChoiceFor(name); ok && c.KeyEnv != "" {
			pm.note += "; it's here because $" + c.KeyEnv + " is set"
		}
		return nil
	}
	if !confirmed {
		pm.confirm = name
		pm.note, pm.noteErr = "press d again to remove "+name+" from your settings", true
		return nil
	}
	changed, err := cfg.RemoveProvider(name)
	if err != nil {
		pm.note, pm.noteErr = err.Error(), true
		return nil
	}
	var where []string
	for _, p := range changed {
		where = append(where, tildePath(p))
	}
	pm.note, pm.noteErr = "removed "+name+" from "+strings.Join(where, " and "), false
	delete(m.modelLists, name)
	m.buildProviders()
	return nil
}

// openWizardFromProviders opens the connect wizard for name ("" adds a
// new provider) and comes back to /providers when it closes.
func (m *model) openWizardFromProviders(name string) tea.Cmd {
	m.provs = nil
	m.wizardReturn = true
	if name == "" {
		return m.openWizard("")
	}
	if _, builtin := providers.ChoiceFor(name); builtin {
		return m.openWizard(name)
	}
	w, detect := newWizard(m.opts.Config, false)
	m.wizard = w
	return tea.Batch(detect, w.editCustom(name, m.opts.Config.Providers[name]))
}

func (m *model) providersView() string {
	pm := m.provs
	w := max(m.width-6, 20)
	budget := m.availablePanelRows()
	var detail []string
	if it, ok := pm.list.selected(); ok {
		if p, ok := it.value.(pickProvider); ok {
			detail = m.providerDetail(p.name)
		}
	}
	// Keep the list navigable even when a provider has a long description.
	detailRows := min(len(detail), max(budget-9-boolRows(pm.note != ""), 0))
	pm.list.height = max(min(12, budget-6-detailRows-boolRows(pm.note != "")), 1)
	head := m.st.accent.Render("Providers")
	if path := tildePath(m.opts.Config.UserConfigPath()); len(path)+12 < w {
		head = spread(head, m.st.dim.Render(path), w)
	}
	lines := []string{
		head,
		pm.list.view(m.st, w),
		m.st.dim.Render(strings.Repeat("─", w)),
	}
	lines = append(lines, detail...)
	if pm.note != "" {
		style := m.st.ok
		if pm.noteErr {
			style = m.st.warn
		}
		lines = append(lines, "", style.Render(pm.note))
	}
	lines = append(lines, "", m.st.dim.Render("↑/↓ move · enter edit · t test · d remove · a add · esc close"))
	return m.st.modal.Width(max(m.width-2, 10)).Render(strings.Join(lines, "\n"))
}

// providerDetail describes the selected provider below the list.
func (m *model) providerDetail(name string) []string {
	cfg := m.opts.Config
	ep := providers.EndpointFor(cfg, name)
	pc := cfg.Providers[name]
	c, builtin := providers.ChoiceFor(name)
	kind := "openai-compatible"
	if builtin {
		kind = c.Title
	}
	row := func(k, v string) string { return m.st.dim.Render(pad(k, 11)) + v }

	var key string
	switch {
	case builtin && c.SignIn:
		row := func(k, v string) string { return m.st.dim.Render(pad(k, 11)) + v }
		auth := m.st.warn.Render("not signed in") + m.st.dim.Render(" · enter to sign in")
		if ep.Token != nil {
			auth = keySource(cfg, c) + m.st.dim.Render(" · d to sign out")
		}
		lines := []string{m.st.accent.Render(name) + m.st.dim.Render(" · "+kind), row("endpoint", ep.BaseURL), row("account", auth)}
		return append(lines, m.modelLines(name, row)...)
	case pc.APIKey != "":
		key = "saved in config " + m.st.dim.Render("(••••"+pc.APIKey[max(len(pc.APIKey)-4, 0):]+")")
	case pc.APIKeyEnv != "":
		key = "from $" + pc.APIKeyEnv
	case builtin && c.Local, !builtin && ep.Key == "":
		key = m.st.dim.Render("none needed")
	case ep.Key != "":
		key = "from $" + c.KeyEnv
	default:
		key = m.st.warn.Render("not set") + m.st.dim.Render(" · enter to add one")
	}
	lines := []string{
		m.st.accent.Render(name) + m.st.dim.Render(" · "+kind),
		row("base URL", ep.BaseURL),
		row("API key", key),
	}
	if ep.Kind == ollama.Name {
		window := m.st.dim.Render(fmt.Sprintf("%d tokens, or the model's maximum if smaller", ollama.DefaultContextLength))
		if pc.ContextLength > 0 {
			window = fmt.Sprintf("%d tokens", pc.ContextLength)
		}
		lines = append(lines, row("context", window+m.st.dim.Render(` · "context_length" in settings`)))
	}
	return append(lines, m.modelLines(name, row)...)
}

// modelLines is the models row, or the error listing them gave.
func (m *model) modelLines(name string, row func(k, v string) string) []string {
	res, loaded := m.modelLists[name]
	switch {
	case !loaded:
		return nil
	case res.err != nil:
		return []string{row("error", m.st.err.Render(res.err.Error()))}
	}
	var ids []string
	for _, md := range res.models {
		if md.Chat {
			ids = append(ids, md.ID)
		}
	}
	more := ""
	if len(ids) > 4 {
		more = m.st.dim.Render(fmt.Sprintf(" +%d more", len(ids)-4))
		ids = ids[:4]
	}
	return []string{row("models", strings.Join(ids, ", ")+more)}
}
