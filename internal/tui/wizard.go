package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"larik/internal/config"
	"larik/internal/providers"
)

type wizStep int

const (
	wizProvider wizStep = iota
	wizConnect
	wizModel
	wizSave
)

var wizStepNames = []string{"Provider", "Connect", "Model", "Save"}

// wizard connects a provider in four steps: choose it, connect (URL or
// key, tested live), pick one of its models, and save.
type wizard struct {
	cfg      *config.Config
	firstRun bool
	step     wizStep
	gen      int // discards replies to abandoned connection tests

	detected map[string]providers.Detection // nil until detection finishes
	provList picker

	choice   providers.Choice
	custom   bool
	savedKey string // a key already available from env or config
	useSaved bool
	fields   []textinput.Model
	labels   []string
	focus    int
	busy     bool
	err      string
	endpoint providers.Endpoint
	pc       config.ProviderConfig // what gets saved for the provider

	models picker
	model  string

	saveRow     int // 0 user config, 1 project, 2 default toggle
	scope       int // 0 user config, 1 project
	makeDefault bool

	done, canceled bool
	result         wizardResult
}

type wizardResult struct {
	Provider string
	Model    string
	Path     string // settings file written, or "" if nothing needed saving
	Default  bool
}

func (r wizardResult) Spec() string { return r.Provider + "/" + r.Model }

type (
	wizDetectedMsg struct {
		found map[string]providers.Detection
	}
	wizTestedMsg struct {
		gen    int
		models []providers.Model
		err    error
	}
)

// Custom provider value in the provider list.
type wizCustom struct{}

func newWizard(cfg *config.Config, firstRun bool) (*wizard, tea.Cmd) {
	w := &wizard{cfg: cfg, firstRun: firstRun}
	w.provList = picker{filterable: true}
	w.buildProviders()
	return w, func() tea.Msg {
		return wizDetectedMsg{providers.Detect(context.Background(), cfg)}
	}
}

// startAt skips the provider step, e.g. from the model picker's
// "Connect Anthropic…" row. Esc still goes back to the provider list.
func (w *wizard) startAt(name string) tea.Cmd {
	c, ok := providers.ChoiceFor(name)
	if !ok {
		return nil
	}
	w.provList.selectWhere(func(it pickItem) bool { v, ok := it.value.(providers.Choice); return ok && v.Name == name })
	return w.enterConnect(c, false)
}

// editCustom opens the connect step for a configured custom provider,
// with its settings filled in.
func (w *wizard) editCustom(name string, pc config.ProviderConfig) tea.Cmd {
	cmd := w.enterConnect(providers.Choice{Title: "OpenAI-compatible server", Desc: "your own endpoint"}, true)
	w.fields[0].SetValue(name)
	w.fields[1].SetValue(pc.BaseURL)
	w.fields[2].SetValue(pc.APIKey)
	return cmd
}

func (w *wizard) buildProviders() {
	var detected, cloud, local []pickItem
	for _, c := range providers.Choices() {
		it := pickItem{label: c.Title, detail: c.Desc, value: c}
		d, known := w.detected[c.Name]
		if c.Local {
			it.detail += " · " + hostOf(c.BaseURL)
		}
		switch {
		case d.Running:
			it.note, it.noteOK = fmt.Sprintf("● running · %d models", d.Models), true
		case d.HasKey:
			it.note, it.noteOK = keySource(w.cfg, c), true
		case known && c.Local:
			it.note = "not running"
		}
		switch {
		case d.Running || d.HasKey:
			it.section = "detected"
			detected = append(detected, it)
		case c.Local:
			it.section = "local"
			local = append(local, it)
		default:
			it.section = "cloud"
			cloud = append(cloud, it)
		}
	}
	items := append(append(detected, cloud...), local...)
	items = append(items, pickItem{section: "custom", label: "OpenAI-compatible…", detail: "any server with /v1/chat/completions", value: wizCustom{}, keep: true})
	prev, hadPrev := w.provList.selected()
	w.provList.items = items
	if hadPrev && w.provList.filter != "" {
		w.provList.selectWhere(func(it pickItem) bool { return it.label == prev.label })
	} else {
		w.provList.home() // detected providers come first
	}
}

func keySource(cfg *config.Config, c providers.Choice) string {
	pc := cfg.Providers[c.Name]
	switch {
	case pc.APIKey != "":
		return "key saved in config"
	case pc.APIKeyEnv != "":
		return "key in $" + pc.APIKeyEnv
	}
	return "key in $" + c.KeyEnv
}

func (w *wizard) newField(label, value, placeholder string, secret bool) {
	f := textinput.New()
	f.Prompt = "› "
	f.Placeholder = placeholder
	f.SetValue(value)
	if secret {
		f.EchoMode = textinput.EchoPassword
		f.EchoCharacter = '•'
	}
	w.fields = append(w.fields, f)
	w.labels = append(w.labels, label)
}

// enterConnect sets up the connect step for a provider.
func (w *wizard) enterConnect(c providers.Choice, custom bool) tea.Cmd {
	w.step, w.choice, w.custom = wizConnect, c, custom
	w.fields, w.labels, w.focus, w.err, w.busy = nil, nil, 0, "", false
	w.savedKey, w.useSaved = "", false
	switch {
	case custom:
		w.newField("Name", "", "e.g. vllm (used as the model prefix)", false)
		w.newField("Base URL", "", "e.g. http://localhost:8000/v1", false)
		w.newField("API key", "", "optional", true)
	case c.Local:
		w.newField("Base URL", providers.EndpointFor(w.cfg, c.Name).BaseURL, c.BaseURL, false)
	default:
		w.savedKey = providers.EndpointFor(w.cfg, c.Name).Key
		w.useSaved = w.savedKey != ""
		w.newField("API key", "", "paste your "+c.Title+" API key", true)
	}
	return w.focusField()
}

func (w *wizard) focusField() tea.Cmd {
	var cmd tea.Cmd
	for i := range w.fields {
		if i == w.focus && !w.useSaved {
			cmd = w.fields[i].Focus()
		} else {
			w.fields[i].Blur()
		}
	}
	return cmd
}

// update handles a message and reports nothing; the host checks done and
// canceled afterwards.
func (w *wizard) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case wizDetectedMsg:
		w.detected = msg.found
		w.buildProviders()
		return nil
	case wizTestedMsg:
		if msg.gen != w.gen || w.step != wizConnect {
			return nil
		}
		w.busy = false
		if msg.err != nil {
			w.err = msg.err.Error()
			return nil
		}
		w.enterModels(msg.models)
		return nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			w.canceled = true
			return nil
		}
		switch w.step {
		case wizProvider:
			return w.providerKey(msg)
		case wizConnect:
			return w.connectKey(msg)
		case wizModel:
			return w.modelKey(msg)
		case wizSave:
			return w.saveKey(msg)
		}
	}
	// Anything else (cursor blinks) goes to the focused field.
	if w.step == wizConnect && w.focus < len(w.fields) {
		var cmd tea.Cmd
		w.fields[w.focus], cmd = w.fields[w.focus].Update(msg)
		return cmd
	}
	return nil
}

func (w *wizard) providerKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" {
		if w.provList.filter != "" {
			w.provList.filter = ""
			w.provList.home()
			return nil
		}
		w.canceled = true
		return nil
	}
	if !w.provList.handleKey(msg) {
		return nil
	}
	it, _ := w.provList.selected()
	switch v := it.value.(type) {
	case providers.Choice:
		return w.enterConnect(v, false)
	case wizCustom:
		return w.enterConnect(providers.Choice{Title: "OpenAI-compatible server", Desc: "your own endpoint"}, true)
	}
	return nil
}

func (w *wizard) connectKey(msg tea.KeyPressMsg) tea.Cmd {
	if w.busy {
		if msg.String() == "esc" { // abandon the test
			w.gen++
			w.busy = false
		}
		return nil
	}
	switch msg.String() {
	case "esc":
		w.step, w.err = wizProvider, ""
		return nil
	case "enter":
		if w.custom && w.focus < len(w.fields)-1 {
			w.focus++
			return w.focusField()
		}
		return w.test()
	case "up", "shift+tab":
		if w.savedKey != "" {
			w.useSaved = true
			return w.focusField()
		}
		if w.focus > 0 {
			w.focus--
			return w.focusField()
		}
		return nil
	case "down", "tab":
		if w.savedKey != "" {
			w.useSaved = false
			return w.focusField()
		}
		if w.focus < len(w.fields)-1 {
			w.focus++
			return w.focusField()
		}
		return nil
	}
	if w.useSaved {
		return nil
	}
	var cmd tea.Cmd
	w.fields[w.focus], cmd = w.fields[w.focus].Update(msg)
	w.err = ""
	return cmd
}

var customName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// test validates the connect form and lists the provider's models.
func (w *wizard) test() tea.Cmd {
	val := func(i int) string { return strings.TrimSpace(w.fields[i].Value()) }
	name := w.choice.Name
	pc := w.cfg.Providers[name]
	switch {
	case w.custom:
		name = strings.ToLower(val(0))
		if !customName.MatchString(name) {
			w.err = "name: use lower-case letters, digits, - or _"
			return nil
		}
		if _, builtin := providers.ChoiceFor(name); builtin {
			w.err = name + " is a built-in provider; pick another name"
			return nil
		}
		if err := checkURL(val(1)); err != nil {
			w.err = "base URL: " + err.Error()
			return nil
		}
		pc = config.ProviderConfig{Type: "openai-compatible", BaseURL: val(1), APIKey: val(2)}
		w.choice.Name = name
	case w.choice.Local:
		u := val(0)
		if err := checkURL(u); err != nil {
			w.err = "base URL: " + err.Error()
			return nil
		}
		pc.BaseURL = u
		if u == w.choice.BaseURL {
			pc.BaseURL = "" // the preset default needs no entry
		}
	default:
		if !w.useSaved {
			key := val(0)
			if key == "" {
				w.err = "paste a key, or set $" + w.choice.KeyEnv + " and start larik again"
				return nil
			}
			pc.APIKey, pc.APIKeyEnv = key, ""
		}
	}
	w.pc = pc
	w.endpoint = providers.EndpointOf(name, pc)
	w.busy, w.err = true, ""
	w.gen++
	gen, ep := w.gen, w.endpoint
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		ms, err := ep.ListModels(ctx)
		return wizTestedMsg{gen: gen, models: ms, err: err}
	}
}

func checkURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("expected http(s)://host[:port]/path")
	}
	return nil
}

func (w *wizard) enterModels(models []providers.Model) {
	w.step = wizModel
	w.models = picker{filterable: true, height: 10, items: modelItems(models, "", "")}
	w.models.extra = func(f string) []pickItem {
		f = strings.TrimSpace(f)
		if f == "" {
			return nil
		}
		for _, m := range models {
			if m.ID == f {
				return nil
			}
		}
		return []pickItem{{label: "Use “" + f + "”", detail: "as the model id", value: f}}
	}
	cur := ""
	if spec := w.cfg.Model; strings.HasPrefix(spec, w.choice.Name+"/") {
		cur = strings.TrimPrefix(spec, w.choice.Name+"/")
	}
	w.models.selectWhere(func(it pickItem) bool {
		id, _ := it.value.(string)
		if cur != "" {
			return id == cur
		}
		return strings.Contains(it.note, "tools")
	})
}

// modelItems turns a model list into picker rows. current marks the
// active model; section names the rows' section.
func modelItems(models []providers.Model, section, current string) []pickItem {
	var items []pickItem
	for _, m := range models {
		if !m.Chat && !m.CapsKnown {
			continue // a guess says it can't chat; don't clutter the list
		}
		it := pickItem{section: section, label: m.ID, detail: modelDetail(m), value: m.ID}
		var caps []string
		switch {
		case !m.Chat:
			it.disabled = true
			caps = append(caps, "can't chat")
		case m.CapsKnown:
			if m.Tools {
				caps = append(caps, "tools")
			} else {
				caps = append(caps, "no tool calling")
			}
			if m.Thinking {
				caps = append(caps, "thinking")
			}
			it.noteOK = m.Tools
		}
		if m.ID == current {
			caps = append([]string{"✓ current"}, caps...)
			it.noteOK = true
		}
		it.note = strings.Join(caps, " · ")
		items = append(items, it)
	}
	return items
}

func modelDetail(m providers.Model) string {
	var parts []string
	if m.Params != "" {
		parts = append(parts, m.Params)
	}
	if m.Size > 0 {
		parts = append(parts, humanBytes(m.Size))
	}
	if m.Context > 0 {
		parts = append(parts, humanTokens(m.Context)+" ctx")
	}
	return strings.Join(parts, " · ")
}

func humanBytes(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	}
	return fmt.Sprintf("%d KB", n/1e3)
}

func humanTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%gM", float64(n/100_000)/10)
	}
	return fmt.Sprintf("%dk", n/1000)
}

func (w *wizard) modelKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "esc" {
		if w.models.filter != "" {
			w.models.filter = ""
			w.models.home()
			return nil
		}
		w.step, w.err = wizConnect, ""
		return w.focusField()
	}
	if !w.models.handleKey(msg) {
		return nil
	}
	it, _ := w.models.selected()
	w.model = it.value.(string)
	w.step = wizSave
	w.saveRow, w.scope = 0, 0
	w.makeDefault = w.firstRun || w.cfg.Model == ""
	return nil
}

func (w *wizard) saveKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		w.step, w.err = wizModel, ""
	case "up", "k":
		w.saveRow = max(w.saveRow-1, 0)
	case "down", "j", "tab":
		w.saveRow = min(w.saveRow+1, 2)
	case "space", " ":
		if w.saveRow == 2 {
			w.makeDefault = !w.makeDefault
		} else {
			w.scope = w.saveRow
		}
	case "enter":
		if w.saveRow < 2 {
			w.scope = w.saveRow
		}
		w.save()
	}
	return nil
}

func (w *wizard) savePath() string {
	if w.scope == 1 {
		return config.LocalSettingsPath(w.cfg.Cwd)
	}
	return w.cfg.UserConfigPath()
}

func (w *wizard) save() {
	res := wizardResult{Provider: w.choice.Name, Model: w.model, Default: w.makeDefault}
	model := ""
	if w.makeDefault {
		model = res.Spec()
	}
	if w.pc != (config.ProviderConfig{}) || model != "" {
		res.Path = w.savePath()
		if err := w.cfg.SaveProvider(res.Path, res.Provider, w.pc, model); err != nil {
			w.err = err.Error()
			return
		}
	}
	w.result, w.done = res, true
}

// preview is the JSON the save step will merge into the settings file.
func (w *wizard) preview() string {
	raw := map[string]any{}
	if w.makeDefault {
		raw["model"] = w.choice.Name + "/" + w.model
	}
	if w.pc != (config.ProviderConfig{}) {
		pc := w.pc
		if pc.APIKey != "" {
			pc.APIKey = "••••" + pc.APIKey[max(len(pc.APIKey)-4, 0):]
		}
		raw["providers"] = map[string]any{w.choice.Name: pc}
	}
	if len(raw) == 0 {
		return ""
	}
	b, _ := json.MarshalIndent(raw, "", "  ")
	return string(b)
}

// view renders the current step to fit width × height.
func (w *wizard) view(st styles, width, height int) string {
	title := st.accent.Render("Connect a provider")
	if w.firstRun {
		title = st.accent.Render("✻ Welcome to larik") + st.dim.Render(" · let's connect a model")
	}
	var steps []string
	for i, n := range wizStepNames {
		switch {
		case wizStep(i) < w.step:
			steps = append(steps, st.ok.Render("✓ "+n))
		case wizStep(i) == w.step:
			steps = append(steps, st.accent.Render("● "+n))
		default:
			steps = append(steps, st.dim.Render("○ "+n))
		}
	}
	head := spread(title, strings.Join(steps, st.dim.Render(" ─ ")), width)
	lines := []string{head, st.dim.Render(strings.Repeat("─", width))}

	var body []string
	var hint string
	switch w.step {
	case wizProvider:
		if w.detected == nil {
			body = append(body, st.dim.Render("looking for local servers and API keys…"))
		}
		body = append(body, w.provList.filterLine(st, "type to filter providers…"), w.provList.view(st, width))
		hint = "↑/↓ move · type to filter · enter next · esc cancel"
		if w.firstRun {
			hint = "↑/↓ move · type to filter · enter next · esc quit"
		}
	case wizConnect:
		body, hint = w.connectView(st, width)
	case wizModel:
		w.models.height = max(min(10, height-10), 3)
		body = append(body,
			st.accent.Render("Choose a model")+st.dim.Render(" · from "+w.choice.Title+" at "+hostOf(w.endpoint.BaseURL)),
			w.models.filterLine(st, "type to filter, or type a model id…"),
			w.models.view(st, width))
		if w.endpoint.IsOllama() {
			body = append(body, "", st.dim.Render("Ollama gives models a small context window by default, which can stop tool calls."),
				st.dim.Render("If the model ignores tools, start Ollama with ")+"OLLAMA_CONTEXT_LENGTH=32768 ollama serve")
		}
		hint = "↑/↓ move · type to filter · enter select · esc back"
	case wizSave:
		body, hint = w.saveView(st)
	}
	lines = append(lines, body...)
	if w.err != "" {
		lines = append(lines, "", st.err.Render("✗ "+w.err))
	}
	lines = append(lines, "", st.dim.Render(hint))
	return strings.Join(lines, "\n")
}

func (w *wizard) connectView(st styles, width int) ([]string, string) {
	c := w.choice
	body := []string{st.accent.Render(c.Title) + st.dim.Render(" · "+c.Desc), ""}
	hint := "enter test and continue · esc back"
	for i := range w.fields {
		w.fields[i].SetWidth(max(width-6, 10))
	}
	switch {
	case w.custom:
		for i, f := range w.fields {
			label := st.dim.Render(w.labels[i])
			if i == w.focus {
				label = st.user.Render(w.labels[i])
			}
			body = append(body, label, "  "+f.View())
		}
		hint = "tab next field · enter continue · esc back"
	case c.Local:
		body = append(body, st.dim.Render("Base URL"), "  "+w.fields[0].View())
		if strings.TrimSpace(w.fields[0].Value()) == c.BaseURL {
			body = append(body, st.dim.Render("  the "+c.Title+" default · no API key needed"))
		}
	default:
		body = append(body, st.dim.Render("API key"))
		if w.savedKey != "" {
			src := strings.TrimPrefix(keySource(w.cfg, c), "key ")
			opts := []string{"Use the key " + src, "Paste a different key"}
			for i, o := range opts {
				sel := (i == 0) == w.useSaved
				if sel {
					body = append(body, st.accent.Render("› "+o))
				} else {
					body = append(body, "  "+o)
				}
			}
			hint = "↑/↓ choose · enter test and continue · esc back"
		}
		if !w.useSaved {
			body = append(body, "  "+w.fields[0].View())
			if w.savedKey == "" && c.KeyEnv != "" {
				body = append(body, st.dim.Render("  or set $"+c.KeyEnv+" in your shell and start larik again"))
			}
		}
	}
	if w.busy {
		body = append(body, "", st.accent.Render("… ")+st.dim.Render("testing connection to "+hostOf(w.endpoint.BaseURL)+" (esc to stop)"))
	}
	return body, hint
}

func (w *wizard) saveView(st styles) ([]string, string) {
	body := []string{
		st.ok.Render("✓ Ready") + st.dim.Render(" · larik will use ") + w.model + st.dim.Render(" on "+w.choice.Title),
		"",
		st.dim.Render("Save to"),
	}
	rows := []string{
		radio(w.scope == 0) + " " + tildePath(w.cfg.UserConfigPath()) + st.dim.Render("  every project"),
		radio(w.scope == 1) + " " + ".larik/settings.local.json" + st.dim.Render("  this project only"),
		check(w.makeDefault) + " Make " + w.model + " the default model",
	}
	for i, r := range rows {
		if i == w.saveRow {
			body = append(body, st.accent.Render("› ")+r)
		} else {
			body = append(body, "  "+r)
		}
	}
	if p := w.preview(); p != "" {
		body = append(body, "", st.dim.Render("Adds to "+tildePath(w.savePath())+":"), st.dim.Render(p))
	} else {
		body = append(body, "", st.dim.Render("Nothing to save: larik will use "+w.model+" for this session only."))
	}
	if w.pc.APIKey != "" && w.scope == 1 {
		body = append(body, st.warn.Render("! the API key will be stored in this project; keep .larik/settings.local.json out of git"))
	}
	return body, "↑/↓ move · space select · enter save and start · esc back"
}

func radio(on bool) string {
	if on {
		return "◉"
	}
	return "○"
}

func check(on bool) string {
	if on {
		return "[x]"
	}
	return "[ ]"
}

// spread puts left and right on one line of width, or on two if they
// don't fit.
func spread(left, right string, width int) string {
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 2 {
		return left + "\n" + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

func tildePath(p string) string {
	if home := homeDir(); home != "" && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// setupModel runs the wizard as its own program, before any session.
type setupModel struct {
	w      *wizard
	init   tea.Cmd
	st     styles
	width  int
	height int
}

// RunSetup runs the connect wizard on its own, for a first start with no
// model configured. It returns the chosen provider/model, or "" if the
// user quit. The choice is saved to a settings file when the user asked.
func RunSetup(cfg *config.Config) (string, error) {
	w, cmd := newWizard(cfg, true)
	s := &setupModel{w: w, init: cmd, st: newStyles(true), width: 80, height: 24}
	if _, err := tea.NewProgram(s).Run(); err != nil {
		return "", err
	}
	if !w.done {
		return "", nil
	}
	return w.result.Spec(), nil
}

func (s *setupModel) Init() tea.Cmd { return tea.Batch(tea.RequestBackgroundColor, s.init) }

func (s *setupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		s.st = newStyles(msg.IsDark())
		return s, nil
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		return s, nil
	}
	cmd := s.w.update(msg)
	if s.w.done || s.w.canceled {
		return s, tea.Quit
	}
	return s, cmd
}

func (s *setupModel) View() tea.View {
	if s.w.done || s.w.canceled {
		return tea.NewView("")
	}
	v := tea.NewView(s.st.modal.Width(max(s.width-2, 20)).Render(s.w.view(s.st, max(s.width-6, 20), s.height)))
	v.WindowTitle = "larik setup"
	v.AltScreen = true // a full-screen step; the terminal is restored after
	return v
}
