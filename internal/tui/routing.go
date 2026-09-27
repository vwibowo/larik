package tui

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/providers"
)

type rtStep int

const (
	rtPreset rtStep = iota
	rtRoles
	rtFallbacks
	rtBudget
	rtSave
)

var rtStepNames = []string{"Preset", "Roles", "Fallbacks", "Budget", "Save"}

// routingWizard sets up model routing: which model each role (worker,
// explore, …) runs on, what to fall back to, and a spending cap. It
// starts from the current config and saves the whole routing at the end.
type routingWizard struct {
	cfg  *config.Config
	main string // the main agent's provider/model
	step rtStep

	lists   map[string]providerModels // nil while loading
	presets picker
	r       config.Routing // what will be saved

	row      int     // cursor on the roles or fallbacks step
	choosing string  // role whose model is being picked, if any
	models   *picker // the model list while choosing

	fields [2]textinput.Model // budget: cap in USD, warn-at percent
	focus  int

	scope   int // 0 user config, 1 this project
	err     string
	path    string
	connect bool // left to connect another provider

	done, canceled bool
}

type presetChoice struct{ p providers.Preset }

func newRoutingWizard(cfg *config.Config, main string, lists map[string]providerModels, start rtStep) *routingWizard {
	w := &routingWizard{cfg: cfg, main: main, lists: lists, r: cfg.Routing(), step: start}
	w.r.Roles = maps.Clone(w.r.Roles)
	w.r.Options = maps.Clone(w.r.Options)
	w.presets.items = []pickItem{
		{label: "Balanced", detail: "keep your main model for planning; cheaper models for subagents", value: presetChoice{providers.PresetBalanced}},
		{label: "Cheapest", detail: "the lowest-priced capable models for every subagent", value: presetChoice{providers.PresetCheapest}},
		{label: "Local and plan first", detail: "Ollama, LM Studio or your ChatGPT plan before paid APIs", value: presetChoice{providers.PresetLocal}},
		{label: "Edit current", detail: "start from your current routing and change it by hand", value: presetChoice{providers.PresetCustom}},
	}
	w.presets.home()
	for i, ph := range []string{"e.g. 2.00, empty for no cap", "80"} {
		f := textinput.New()
		f.Placeholder = ph
		f.Prompt = ""
		w.fields[i] = f
	}
	if b := w.r.Budget; b.SessionUSD > 0 {
		w.fields[0].SetValue(strconv.FormatFloat(b.SessionUSD, 'f', -1, 64))
	}
	if b := w.r.Budget; b.WarnAt > 0 {
		w.fields[1].SetValue(strconv.FormatFloat(b.WarnAt*100, 'f', -1, 64))
	}
	if start == rtBudget {
		w.fields[0].Focus()
	}
	return w
}

// openRouting opens the routing wizard at step, listing models first if
// they haven't been.
func (m *model) openRouting(step rtStep) tea.Cmd {
	m.routing = newRoutingWizard(m.opts.Config, m.agent.ProviderName()+"/"+m.agent.Model(), m.modelLists, step)
	if m.modelLists == nil {
		return m.loadModels()
	}
	m.routing.addConnectRow()
	return nil
}

func (m *model) handleRouting(msg tea.KeyPressMsg) tea.Cmd {
	w := m.routing
	cmd := w.update(msg)
	if !w.done && !w.canceled {
		return cmd
	}
	m.routing = nil
	switch {
	case w.connect:
		return m.openWizard("")
	case w.canceled:
		return nil
	}
	return m.println(m.st.ok.Render("✓ Model routing saved") + m.st.dim.Render(" to "+tildePath(w.path)+" · applies to the next request\n"+routingSummary(w.cfg)))
}

// setLists supplies the model lists once they load.
func (w *routingWizard) setLists(lists map[string]providerModels) {
	w.lists = lists
	w.addConnectRow()
}

// addConnectRow offers to connect a provider when there is little to route between.
func (w *routingWizard) addConnectRow() {
	if len(w.options()) > 0 && len(w.providersWithModels()) > 1 {
		return
	}
	for _, it := range w.presets.items {
		if it.value == (pickConnect{}) {
			return
		}
	}
	w.presets.items = append(w.presets.items, pickItem{section: " ", label: "+ Connect another provider…", detail: "routing pays off with a cheap model next to your main one", value: pickConnect{}})
}

func (w *routingWizard) providersWithModels() []string {
	var names []string
	for n, l := range w.lists {
		if l.err == nil && len(l.models) > 0 {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}

// options lists every model the usable providers offer.
func (w *routingWizard) options() []providers.ModelOption {
	var out []providers.ModelOption
	for _, n := range w.providersWithModels() {
		for _, md := range w.lists[n].models {
			out = append(out, providers.ModelOption{Provider: n, Model: md})
		}
	}
	return out
}

// roleRows are the roles shown on the roles step.
func (w *routingWizard) roleRows() []string {
	names := slices.Clone(providers.Roles)
	var extra []string
	for n := range w.r.Roles {
		if !slices.Contains(names, n) {
			extra = append(extra, n)
		}
	}
	slices.Sort(extra)
	return append(names, extra...)
}

// fallbackRows are the roles that run on a model of their own.
func (w *routingWizard) fallbackRows() []string {
	var out []string
	for _, n := range w.roleRows() {
		if w.r.Roles[n] != "" {
			out = append(out, n)
		}
	}
	return out
}

func (w *routingWizard) update(msg tea.KeyPressMsg) tea.Cmd {
	w.err = ""
	if w.models != nil {
		return w.chooseKey(msg)
	}
	switch w.step {
	case rtPreset:
		if msg.String() == "esc" {
			w.canceled = true
			return nil
		}
		if !w.presets.handleKey(msg) {
			return nil
		}
		it, _ := w.presets.selected()
		switch v := it.value.(type) {
		case pickConnect:
			w.connect, w.canceled = true, true
		case presetChoice:
			if v.p != providers.PresetCustom {
				if w.lists == nil {
					w.err = "still listing your providers' models; try again in a moment"
					return nil
				}
				s := providers.SuggestRouting(v.p, w.options(), w.main)
				if len(s.Roles) == 0 {
					w.err = "found nothing cheaper than " + w.main + " to route to; connect another provider or pick models by hand"
				}
				w.r.Roles, w.r.Fallbacks, w.r.Options = s.Roles, s.Fallbacks, s.Options
			}
			w.step, w.row = rtRoles, 0
		}
	case rtRoles:
		if rows := w.roleRows(); w.row < len(rows) && w.optionKey(msg.String(), rows[w.row]) {
			return nil
		}
		return w.listKey(msg, w.roleRows(), rtPreset, rtFallbacks, func(role string) {
			delete(w.r.Roles, role)
			delete(w.r.Options, role)
		})
	case rtFallbacks:
		return w.listKey(msg, w.fallbackRows(), rtRoles, rtBudget, func(role string) {
			if fb := w.r.Fallbacks[role]; len(fb) > 0 {
				w.r.Fallbacks[role] = fb[:len(fb)-1]
			}
		})
	case rtBudget:
		return w.budgetKey(msg)
	case rtSave:
		switch msg.String() {
		case "esc":
			w.step = rtBudget
			return w.fields[w.focus].Focus()
		case "up", "k", "down", "j", "tab", "space", " ":
			w.scope = 1 - w.scope
		case "enter":
			w.save()
		}
	}
	return nil
}

// optionKey changes a role's limits on the roles step: w toggles the
// worktree, + and - move the turn cap in steps of 10. It reports whether
// the key was one of these.
func (w *routingWizard) optionKey(k, role string) bool {
	if role == providers.RoleCompact {
		return false // compaction isn't a subagent
	}
	o := w.r.Options[role]
	switch k {
	case "w":
		if o.Isolation == "worktree" {
			o.Isolation = ""
		} else {
			o.Isolation = "worktree"
		}
	case "+", "=":
		if o.MaxTurns == 0 {
			return true // already the default, 100
		}
		o.MaxTurns += 10
		if o.MaxTurns >= 100 {
			o.MaxTurns = 0
		}
	case "-":
		if o.MaxTurns == 0 {
			o.MaxTurns = 100
		}
		o.MaxTurns = max(o.MaxTurns-10, 10)
	default:
		return false
	}
	if w.r.Options == nil {
		w.r.Options = map[string]config.RoleOption{}
	}
	w.r.Options[role] = o
	return true
}

// optionNote describes a role's limits, or "" for the defaults.
func optionNote(o config.RoleOption) string {
	var parts []string
	if o.Isolation == "worktree" {
		parts = append(parts, "worktree")
	}
	if o.MaxTurns > 0 {
		parts = append(parts, fmt.Sprintf("%d turns", o.MaxTurns))
	}
	return strings.Join(parts, " · ")
}

// listKey drives the roles and fallbacks steps: a row per role, then
// "Continue".
func (w *routingWizard) listKey(msg tea.KeyPressMsg, rows []string, back, next rtStep, clear func(role string)) tea.Cmd {
	switch msg.String() {
	case "esc":
		w.step, w.row = back, 0
	case "up", "k":
		w.row = max(w.row-1, 0)
	case "down", "j", "tab":
		w.row = min(w.row+1, len(rows))
	case "backspace", "delete", "d":
		if w.row < len(rows) {
			clear(rows[w.row])
		}
	case "right", "n":
		w.advance(next)
	case "enter":
		if w.row == len(rows) {
			w.advance(next)
			break
		}
		w.openChooser(rows[w.row])
	}
	return nil
}

func (w *routingWizard) advance(next rtStep) {
	w.step, w.row = next, 0
	if next == rtFallbacks && len(w.fallbackRows()) == 0 {
		w.step = rtBudget // nothing runs on a model of its own
	}
	if w.step == rtBudget {
		w.focus = 0
		w.fields[0].Focus()
		w.fields[1].Blur()
	}
}

// openChooser lists models for role (roles step) or for its fallback
// chain (fallbacks step).
func (w *routingWizard) openChooser(role string) {
	p := &picker{filterable: true, height: 12}
	if w.step == rtRoles {
		p.items = append(p.items, pickItem{label: "inherit", detail: "same as the main model (" + w.main + ")", value: ""})
	}
	for _, n := range w.providersWithModels() {
		title := n
		if c, ok := providers.ChoiceFor(n); ok {
			title = c.Title
		}
		rows := modelItems(w.lists[n].models, title, "")
		for i := range rows {
			spec := n + "/" + rows[i].label
			rows[i].value = spec
			if pn := providers.PriceNote(spec); pn != "" {
				rows[i].detail = strings.TrimSpace(rows[i].detail + "  " + pn)
			}
			if spec == w.r.Roles[role] {
				rows[i].note, rows[i].noteOK = "✓ current", true
			}
		}
		p.items = append(p.items, rows...)
	}
	if w.lists == nil {
		p.items = append(p.items, pickItem{label: "loading models…", disabled: true})
	}
	p.extra = func(f string) []pickItem {
		f = strings.TrimSpace(f)
		if !strings.Contains(f, "/") {
			return nil
		}
		return []pickItem{{label: "Use “" + f + "”", detail: "as provider/model", value: f}}
	}
	p.selectWhere(func(it pickItem) bool { return it.value == w.r.Roles[role] })
	w.choosing, w.models = role, p
}

func (w *routingWizard) chooseKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" {
		w.models, w.choosing = nil, ""
		return nil
	}
	if !w.models.handleKey(msg) {
		return nil
	}
	it, _ := w.models.selected()
	spec, _ := it.value.(string)
	spec = strings.TrimSpace(spec)
	if providers.IsRole(w.cfg, spec) && spec != "" {
		w.err = "pick a model, not a role"
		return nil
	}
	role := w.choosing
	switch w.step {
	case rtRoles:
		if w.r.Roles == nil {
			w.r.Roles = map[string]string{}
		}
		w.r.Roles[role] = spec
		if spec == "" {
			delete(w.r.Roles, role)
		}
	case rtFallbacks:
		if spec != "" && spec != w.r.Roles[role] && !slices.Contains(w.r.Fallbacks[role], spec) {
			if w.r.Fallbacks == nil {
				w.r.Fallbacks = map[string][]string{}
			}
			w.r.Fallbacks[role] = append(w.r.Fallbacks[role], spec)
		}
	}
	w.models, w.choosing = nil, ""
	return nil
}

func (w *routingWizard) budgetKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		w.fields[w.focus].Blur()
		w.step, w.row = rtFallbacks, 0
		if len(w.fallbackRows()) == 0 {
			w.step = rtRoles
		}
		return nil
	case "tab", "down", "up", "shift+tab":
		w.fields[w.focus].Blur()
		w.focus = 1 - w.focus
		return w.fields[w.focus].Focus()
	case "enter":
		b, err := parseBudget(w.fields[0].Value(), w.fields[1].Value())
		if err != nil {
			w.err = err.Error()
			return nil
		}
		w.r.Budget = b
		w.fields[w.focus].Blur()
		w.step = rtSave
		return nil
	}
	var cmd tea.Cmd
	w.fields[w.focus], cmd = w.fields[w.focus].Update(msg)
	return cmd
}

// parseBudget reads the budget fields: dollars (a leading $ is fine) and
// a warning threshold in percent.
func parseBudget(capText, warnText string) (config.Budget, error) {
	var b config.Budget
	if s := strings.TrimPrefix(strings.TrimSpace(capText), "$"); s != "" {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || f < 0 {
			return b, fmt.Errorf("the budget is a number of dollars, like 2.50")
		}
		b.SessionUSD = f
	}
	if s := strings.TrimSuffix(strings.TrimSpace(warnText), "%"); s != "" {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil || f <= 0 || f >= 100 {
			return b, fmt.Errorf("warn at is a percentage between 1 and 99")
		}
		if f != 80 {
			b.WarnAt = f / 100
		}
	}
	return b, nil
}

func (w *routingWizard) savePath() string {
	if w.scope == 1 {
		return config.LocalSettingsPath(w.cfg.Cwd)
	}
	return w.cfg.UserConfigPath()
}

func (w *routingWizard) save() {
	// Keep fallbacks only for roles that still have a model.
	fb := map[string][]string{}
	for role, list := range w.r.Fallbacks {
		if w.r.Roles[role] != "" && len(list) > 0 {
			fb[role] = list
		}
	}
	w.r.Fallbacks = fb
	w.path = w.savePath()
	if err := w.cfg.SaveRouting(w.path, w.r); err != nil {
		w.err = err.Error()
		return
	}
	w.done = true
}

// routingSummary is one line per configured role, for messages and /routing.
func routingSummary(cfg *config.Config) string {
	r := cfg.Routing()
	var lines []string
	for _, n := range providers.RoleNames(cfg) {
		spec, opts := r.Roles[n], optionNote(r.Options[n])
		if spec == "" && opts == "" {
			continue
		}
		shown := spec
		if spec == "" {
			shown = "inherit"
		}
		line := fmt.Sprintf("  %-8s %s", n, shown)
		if pn := providers.PriceNote(spec); pn != "" {
			line += "  (" + pn + ")"
		}
		if fb := r.Fallbacks[n]; len(fb) > 0 {
			line += "  → " + strings.Join(fb, " → ")
		}
		if opts != "" {
			line += "  [" + opts + "]"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, "  every role inherits the main model")
	}
	if b := r.Budget; b.SessionUSD > 0 {
		lines = append(lines, fmt.Sprintf("  budget   $%.2f per session, warn at %.0f%%", b.SessionUSD, b.WarnFraction()*100))
	}
	return strings.Join(lines, "\n")
}

// view renders the current step to fit width × height.
func (w *routingWizard) view(st styles, width, height int) string {
	var steps []string
	for i, n := range rtStepNames {
		switch {
		case rtStep(i) < w.step:
			steps = append(steps, st.ok.Render("✓ "+n))
		case rtStep(i) == w.step:
			steps = append(steps, st.accent.Render("● "+n))
		default:
			steps = append(steps, st.dim.Render("○ "+n))
		}
	}
	lines := []string{spread(st.accent.Render("Model routing"), strings.Join(steps, st.dim.Render(" ─ ")), width), st.dim.Render(strings.Repeat("─", width))}

	var body []string
	hint := ""
	switch {
	case w.models != nil:
		what := "Model for " + w.choosing
		if w.step == rtFallbacks {
			what = "Add a fallback for " + w.choosing
		}
		w.models.height = max(min(12, height-10), 3)
		body = append(body, st.accent.Render(what)+st.dim.Render(" · "+providers.RoleHints[w.choosing]),
			w.models.filterLine(st, "type to filter, or type provider/model…"), w.models.view(st, width))
		hint = "↑/↓ move · type to filter · enter select · esc back"
	case w.step == rtPreset:
		body = append(body,
			"Plan and review on your main model, "+st.accent.Render(w.main)+", and hand routine work to cheaper ones.",
			st.dim.Render("Subagents run on the worker and explore roles; the main agent can also pick a role per task."), "")
		if w.lists == nil {
			body = append(body, st.dim.Render("listing your providers' models…"))
		}
		body = append(body, w.presets.view(st, width))
		hint = "↑/↓ move · enter choose · esc cancel"
	case w.step == rtRoles:
		body = append(body, st.dim.Render("A role without a model uses the main model."), "")
		body = append(body, w.rowsView(st, w.roleRows(), func(role string) string {
			spec := w.r.Roles[role]
			v := spec
			if spec == "" {
				v = st.dim.Render("inherit")
			} else if pn := providers.PriceNote(spec); pn != "" {
				v += st.dim.Render("  " + pn)
			}
			if n := optionNote(w.r.Options[role]); n != "" {
				v += st.warn.Render("  [" + n + "]")
			}
			return v
		})...)
		if w.r.Roles[providers.RoleCompact] != "" {
			body = append(body, "", st.dim.Render("With prompt caching, compacting on the main model can cost less than a cheaper model reading the whole conversation."))
		}
		body = append(body, "", st.dim.Render("worktree: the subagent edits its own git branch; the main agent reviews and merges it."))
		hint = "↑/↓ move · enter change · d inherit · w worktree · +/- turns · n next · esc back"
	case w.step == rtFallbacks:
		body = append(body, st.dim.Render("When a model is rate-limited, out of quota or down, larik switches to the next one before any output."), "")
		body = append(body, w.rowsView(st, w.fallbackRows(), func(role string) string {
			chain := append([]string{w.r.Roles[role]}, w.r.Fallbacks[role]...)
			if len(chain) == 1 {
				return chain[0] + st.dim.Render("  no fallback")
			}
			return strings.Join(chain, st.dim.Render(" → "))
		})...)
		hint = "↑/↓ move · enter add · d remove last · n next · esc back"
	case w.step == rtBudget:
		for i := range w.fields {
			w.fields[i].SetWidth(max(width-6, 10))
		}
		labels := []string{"Session budget (USD)", "Warn at (% of budget)"}
		for i, f := range w.fields {
			label := st.dim.Render(labels[i])
			if i == w.focus {
				label = st.user.Render(labels[i])
			}
			body = append(body, label, "  "+f.View())
		}
		body = append(body, "", st.dim.Render("larik stops before a request once the session, subagents included, has spent this much."),
			st.dim.Render("Local models and plan-included models count as free."))
		hint = "tab next field · enter continue · esc back"
	case w.step == rtSave:
		preview := *w.cfg
		preview.Roles, preview.Fallbacks, preview.RoleOptions, preview.Budget = w.r.Roles, w.r.Fallbacks, w.r.Options, w.r.Budget
		body = append(body, st.ok.Render("✓ Ready")+st.dim.Render(" · main model "+w.main), routingSummary(&preview), "", st.dim.Render("Save to"))
		rows := []string{
			radio(w.scope == 0) + " " + tildePath(w.cfg.UserConfigPath()) + st.dim.Render("  every project"),
			radio(w.scope == 1) + " .larik/settings.local.json" + st.dim.Render("  this project only"),
		}
		for i, r := range rows {
			if i == w.scope {
				body = append(body, st.accent.Render("› ")+r)
			} else {
				body = append(body, "  "+r)
			}
		}
		hint = "↑/↓ choose · enter save · esc back"
	}
	lines = append(lines, body...)
	if w.err != "" {
		lines = append(lines, "", st.err.Render("✗ "+w.err))
	}
	lines = append(lines, "", st.dim.Render(hint))
	return strings.Join(lines, "\n")
}

// rowsView renders one row per role plus "Continue", with the cursor.
func (w *routingWizard) rowsView(st styles, rows []string, value func(role string) string) []string {
	var out []string
	for i, role := range rows {
		line := pad(role, 9) + value(role)
		if hint := providers.RoleHints[role]; hint != "" && i == w.row {
			line += st.dim.Render("  · " + hint)
		}
		if i == w.row {
			out = append(out, st.accent.Render("› ")+line)
		} else {
			out = append(out, "  "+line)
		}
	}
	cont := "Continue →"
	if w.row == len(rows) {
		out = append(out, "", st.accent.Render("› "+cont))
	} else {
		out = append(out, "", "  "+st.dim.Render(cont))
	}
	return out
}
