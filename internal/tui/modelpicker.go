package tui

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/providers"
)

// availableEfforts lists distinct levels the selected model's adapter can send.
// Unknown capabilities stay on provider default rather than guessing support.
func (m *model) availableEfforts(v pickModel) []llm.Effort {
	defaultOnly := []llm.Effort{llm.EffortDefault}
	basic := []llm.Effort{llm.EffortDefault, llm.EffortLow, llm.EffortMedium, llm.EffortHigh}
	kind := providers.EndpointFor(m.opts.Config, v.provider)
	for _, candidate := range m.modelLists[v.provider].models {
		if candidate.ID == v.model && len(candidate.Efforts) > 0 {
			levels := append([]llm.Effort(nil), defaultOnly...)
			for _, e := range []llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax} {
				if slices.Contains(candidate.Efforts, e) {
					levels = append(levels, e)
				}
			}
			return levels
		}
	}
	switch {
	case kind.Kind == providers.ClaudeCLI:
		// Claude Code reports effort support per model, and its smallest
		// models take none: a listed model that offered no levels above
		// means exactly that, so don't offer any.
		for _, candidate := range m.modelLists[v.provider].models {
			if candidate.ID == v.model && candidate.CapsKnown {
				return defaultOnly
			}
		}
		return []llm.Effort{llm.EffortDefault, llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortMax}
	case kind.IsOllama():
		for _, candidate := range m.modelLists[v.provider].models {
			if candidate.ID == v.model && candidate.CapsKnown && candidate.Thinking {
				return basic
			}
		}
	case kind.Kind == "gemini":
		if strings.HasPrefix(v.model, "gemini-3") {
			return basic // xhigh and max both map to high in the adapter
		}
	case kind.Kind == "anthropic":
		if strings.HasPrefix(v.model, "claude-") {
			if strings.Contains(v.model, "claude-3") || strings.Contains(v.model, "-4-0") ||
				strings.Contains(v.model, "-4-1") || strings.Contains(v.model, "-4-5") ||
				strings.Contains(v.model, "sonnet-4-2") || strings.Contains(v.model, "opus-4-2") {
				return basic // legacy thinking budgets
			}
			if strings.Contains(v.model, "-5") || strings.Contains(v.model, "-4-") {
				return []llm.Effort{llm.EffortDefault, llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortMax}
			}
		}
	case kind.Kind == "openai", kind.Kind == providers.Codex:
		if strings.Contains(v.model, "chat-latest") {
			break
		}
		if strings.HasPrefix(v.model, "gpt-5") || strings.HasPrefix(v.model, "gpt-6") || strings.Contains(v.model, "codex") {
			return []llm.Effort{llm.EffortDefault, llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh}
		}
		if strings.HasPrefix(v.model, "o1") || strings.HasPrefix(v.model, "o3") || strings.HasPrefix(v.model, "o4") {
			return basic
		}
	}
	return defaultOnly
}

func (mp *modelPicker) selectedEfforts(m *model) []llm.Effort {
	if it, ok := mp.list.selected(); ok {
		if v, ok := it.value.(pickModel); ok {
			return m.availableEfforts(v)
		}
	}
	return []llm.Effort{llm.EffortDefault}
}

// Keep the chosen level across rows when possible; otherwise use default.
func (mp *modelPicker) syncEffort(m *model, previous llm.Effort) {
	mp.effort = 0
	for i, e := range mp.selectedEfforts(m) {
		if e == previous {
			mp.effort = i
			break
		}
	}
}

// modelPicker is the /model dropdown: models grouped by provider, an
// effort row, and ways into the connect wizard.
type modelPicker struct {
	list    picker
	effort  int // index into selectedEfforts for the highlighted model
	loading bool
}

// providerModels is one provider's model list, or why it couldn't be read.
type providerModels struct {
	models []providers.Model
	err    error
}

type modelsLoadedMsg struct{ lists map[string]providerModels }

// Values of picker rows.
type (
	pickModel   struct{ provider, model string }
	pickConnect struct{ provider string }
	pickAdd     struct{}
)

func (m *model) openModelPicker() tea.Cmd {
	mp := &modelPicker{list: picker{filterable: true}, loading: m.modelLists == nil}
	mp.list.extra = func(f string) []pickItem {
		f = strings.TrimSpace(f)
		if !strings.Contains(f, "/") {
			return nil
		}
		provider, id, _ := strings.Cut(f, "/")
		return []pickItem{{label: "Use “" + f + "”", detail: "as provider/model", value: pickModel{provider, id}}}
	}
	m.mpick = mp
	m.buildModelList()
	mp.syncEffort(m, m.agent.Effort())
	if m.modelLists != nil {
		return nil
	}
	return m.loadModels()
}

// loadModels lists models from every usable provider in parallel.
func (m *model) loadModels() tea.Cmd {
	cfg, current := m.opts.Config, m.agent.ProviderName()
	return func() tea.Msg {
		names := providers.Usable(cfg)
		if !slices.Contains(names, current) {
			names = append(names, current)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		var (
			mu  sync.Mutex
			wg  sync.WaitGroup
			out = map[string]providerModels{}
		)
		for _, n := range names {
			wg.Go(func() {
				ms, err := providers.EndpointFor(cfg, n).ListModels(ctx)
				mu.Lock()
				out[n] = providerModels{ms, err}
				mu.Unlock()
			})
		}
		wg.Wait()
		return modelsLoadedMsg{out}
	}
}

// buildModelList fills the picker from the cached model lists, keeping
// the cursor on the same row when it can.
func (m *model) buildModelList() {
	mp := m.mpick
	if mp == nil {
		return
	}
	curProvider, curModel := m.agent.ProviderName(), m.agent.Model()
	names := providers.Usable(m.opts.Config)
	if !slices.Contains(names, curProvider) {
		names = append([]string{curProvider}, names...)
	}

	var items []pickItem
	for _, n := range names {
		title := n
		c, builtin := providers.ChoiceFor(n)
		if builtin {
			title = c.Title
		}
		res, loaded := m.modelLists[n]
		_, configured := m.opts.Config.Providers[n]
		switch {
		case !loaded:
			if n == curProvider {
				items = append(items, pickItem{section: title, label: curModel, note: "✓ current", noteOK: true, value: pickModel{n, curModel}})
			}
			if mp.loading {
				items = append(items, pickItem{section: title, label: "loading models…", disabled: true})
			}
			continue
		case res.err != nil && builtin && c.Local && !configured && n != curProvider:
			continue // a local server that isn't running
		case res.err != nil:
			if n == curProvider {
				items = append(items, pickItem{section: title, label: curModel, note: "✓ current", noteOK: true, value: pickModel{n, curModel}})
			}
			items = append(items, pickItem{section: title, label: "couldn't list models", detail: res.err.Error(), disabled: true})
			continue
		}
		cur := ""
		if n == curProvider {
			cur = curModel
		}
		rows := modelItems(res.models, title, cur)
		if cur != "" && !containsModel(res.models, cur) {
			rows = append([]pickItem{{section: title, label: cur, note: "✓ current", noteOK: true}}, rows...)
		}
		for i := range rows {
			rows[i].value = pickModel{n, rows[i].label}
		}
		items = append(items, rows...)
		if providers.EndpointFor(m.opts.Config, n).IsOllama() {
			items = append(items, pickItem{section: title, label: "↓ Download a model…", detail: "from ollama.com", value: pickConnect{n}})
		}
	}
	for _, c := range providers.Choices() {
		if c.Local || slices.Contains(names, c.Name) {
			continue
		}
		switch {
		case c.Catwalk, c.Name == "anthropic", c.Name == "openai", c.Name == "gemini", c.Name == providers.Codex, c.Name == "nvidia-nim":
			items = append(items, pickItem{section: "not set up", label: "+ Connect " + c.Title + "…", detail: c.Desc, value: pickConnect{c.Name}})
		}
	}
	items = append(items, pickItem{section: " ", label: "+ Add provider…", detail: "setup wizard: cloud, local or custom", value: pickAdd{}, keep: true})

	prev, hadPrev := mp.list.selected()
	mp.list.items = items
	switch {
	case hadPrev && prev.value != nil:
		mp.list.selectWhere(func(it pickItem) bool { return it.value == prev.value })
	default:
		mp.list.selectWhere(func(it pickItem) bool { return it.value == pickModel{curProvider, curModel} })
	}
}

func (m *model) handleModelPickerKey(msg tea.KeyPressMsg) tea.Cmd {
	mp := m.mpick
	switch msg.String() {
	case "esc", "ctrl+c":
		if mp.list.filter != "" && msg.String() == "esc" {
			mp.list.filter = ""
			mp.list.home()
			return nil
		}
		m.mpick = nil
		return nil
	case "left":
		mp.effort = max(mp.effort-1, 0)
		return nil
	case "right":
		mp.effort = min(mp.effort+1, len(mp.selectedEfforts(m))-1)
		return nil
	}
	levels := mp.selectedEfforts(m)
	previous := levels[min(mp.effort, len(levels)-1)]
	if !mp.list.handleKey(msg) {
		mp.syncEffort(m, previous)
		return nil
	}
	it, _ := mp.list.selected()
	m.mpick = nil
	switch v := it.value.(type) {
	case pickModel:
		return m.switchModel(v.provider+"/"+v.model, levels[min(mp.effort, len(levels)-1)])
	case pickConnect:
		return m.openWizard(v.provider)
	case pickAdd:
		return m.openWizard("")
	}
	return nil
}

// switchModel resolves spec and makes it the agent's model.
func (m *model) switchModel(spec string, effort llm.Effort) tea.Cmd {
	r, err := providers.Resolve(m.opts.Config, spec)
	if err != nil {
		return m.println(m.st.err.Render(err.Error()))
	}
	if !slices.Contains(m.availableEfforts(pickModel{r.Provider.Name(), r.Model}), effort) {
		effort = llm.EffortDefault
	}
	if err := m.opts.Config.SetUserSettings(map[string]any{"model": r.String(), "effort": string(effort)}); err != nil {
		return m.println(m.st.err.Render("couldn't save model: " + err.Error()))
	}
	m.opts.Config.Model, m.opts.Config.Effort = r.String(), effort
	changed := r.Provider.Name() != m.agent.ProviderName() || r.Model != m.agent.Model()
	if changed {
		m.agent.SetModel(r.Provider, r.Model, r.Runtime)
	}
	m.agent.SetEffort(effort)
	m.stats = m.agent.Stats()
	msg := "switched to " + r.String()
	if !changed {
		msg = "model: " + r.String()
	}
	if effort != llm.EffortDefault {
		msg += " · effort " + string(effort)
	}
	return m.println(m.st.dim.Render(msg + " · saved as default"))
}

func (m *model) modelPickerView() string {
	mp := m.mpick
	w := max(m.width-6, 20)
	mp.list.height = max(min(14, m.availablePanelRows()-7), 1)
	head := spread(m.st.accent.Render("Switch model"), m.st.dim.Render("current: "+m.agent.ProviderName()+"/"+m.agent.Model()), w)

	var eff []string
	for i, e := range mp.selectedEfforts(m) {
		name := string(e)
		if e == llm.EffortDefault {
			name = "default"
		}
		if i == mp.effort {
			eff = append(eff, m.st.accent.Render("["+name+"]"))
		} else {
			eff = append(eff, m.st.dim.Render(name))
		}
	}
	lines := []string{
		head,
		mp.list.filterLine(m.st, "type to filter, or provider/model…"),
		mp.list.view(m.st, w),
		m.st.dim.Render(strings.Repeat("─", w)),
		m.st.dim.Render("Effort  ◂ ") + strings.Join(eff, " ") + m.st.dim.Render(" ▸"),
		m.st.dim.Render("↑/↓ move · type to filter · ←/→ effort · enter switch · esc close"),
	}
	return m.st.modal.Width(max(m.width-2, 10)).Render(strings.Join(lines, "\n"))
}

// openWizard shows the connect wizard, starting at provider if one is named.
func (m *model) openWizard(provider string) tea.Cmd {
	w, detect := newWizard(m.opts.Config, false)
	m.wizard = w
	cmds := []tea.Cmd{detect}
	if provider != "" {
		cmds = append(cmds, w.startAt(provider))
	}
	return tea.Batch(cmds...)
}

func (m *model) handleWizard(msg tea.Msg) tea.Cmd {
	w := m.wizard
	cmd := w.update(msg)
	if !w.canceled && !w.done {
		return cmd
	}
	m.wizard = nil
	var back tea.Cmd
	if m.wizardReturn {
		m.wizardReturn = false
		back = m.openProviders()
	}
	if w.canceled {
		return back
	}
	m.modelLists = nil // new provider: list again next time
	note := ""
	if w.result.Path != "" {
		note = " · saved to " + tildePath(w.result.Path)
	}
	if len(m.opts.Config.Routing().Roles) == 0 && connectedCount(m.opts.Config) > 1 {
		note += "\n" + m.st.dim.Render("tip: /routing runs subagents on a cheaper model while this one plans")
	}
	return tea.Sequence(
		m.println(m.st.ok.Render("✓ Connected "+w.result.Provider)+m.st.dim.Render(note)),
		m.switchModel(w.result.Spec(), m.agent.Effort()),
		back,
	)
}

func containsModel(ms []providers.Model, id string) bool {
	for _, x := range ms {
		if x.ID == id {
			return true
		}
	}
	return false
}

// connectedCount counts providers set up or with a key, leaving out local
// servers that are merely looked for.
func connectedCount(cfg *config.Config) int {
	n := 0
	for _, name := range providers.Usable(cfg) {
		_, configured := cfg.Providers[name]
		if c, ok := providers.ChoiceFor(name); ok && c.Local && !configured {
			continue
		}
		n++
	}
	return n
}
