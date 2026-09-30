package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"larik/internal/permission"
)

// command describes a slash command for /help and the command palette.
type command struct {
	name    string
	args    string // e.g. "[provider/model]"; <required> arguments are completed, not run
	desc    string
	section string
	key     string // shortcut shown in the palette
}

var commands = []command{
	{"/model", "[provider/model]", "pick a model from a list, or switch directly (e.g. /model openai/gpt-5.5)", "model and mode", "alt+p"},
	{"/mode", "[mode]", "pick the permission mode: default, accept-edits, plan, yolo", "model and mode", "shift+tab"},
	{"/effort", "[level]", "show or set reasoning effort: low medium high xhigh max default", "model and mode", ""},
	{"/connect", "[provider]", "set up a provider: pick it, connect, choose a model, save", "model and mode", ""},
	{"/providers", "", "see every provider's status; edit, test, remove or add one", "model and mode", ""},
	{"/routing", "[role=provider/model]", "cheaper models for subagents: roles, fallbacks and a session budget", "model and mode", ""},
	{"/compact", "", "summarize the conversation to free context", "conversation", ""},
	{"/clear", "", "start a fresh context (history stays in the session file)", "conversation", ""},
	{"/undo", "", "revert file changes from the last turn that made any", "conversation", ""},
	{"/rewind", "[n]", "list prompts, or branch off just before prompt n to redo it", "conversation", ""},
	{"/todos", "", "the model's task list for this work", "conversation", ""},
	{"/copy", "", "copy the last reply to the clipboard", "conversation", ""},
	{"/init", "[focus]", "study the project and write AGENTS.md, or improve the existing one", "conversation", ""},
	{"/export", "[file]", "save this session as Markdown (default: larik-<session>.md here)", "sessions", ""},
	{"/cost", "", "token usage and cost for this session", "conversation", ""},
	{"/sessions", "", "choose a session from this directory", "sessions", ""},
	{"/resume", "[id]", "choose a session, or switch by ID", "sessions", ""},
	{"/new", "", "start a new session", "sessions", ""},
	{"/fork", "", "branch the conversation into a new session and continue there", "sessions", ""},
	{"/mcp", "[approve|login|logout <name>]", "MCP servers, their tools, prompts and resources; approve a project server, or sign in to one", "tools", ""},
	{"/hooks", "[approve]", "configured lifecycle hooks; approve the project's shared hooks", "tools", ""},
	{"/skills", "", "list available skills", "tools", ""},
	{"/agents", "", "list subagents the model can delegate to", "tools", ""},
	{"/lsp", "", "language servers and their status", "tools", ""},
	{"/sandbox", "", "bash sandbox status", "tools", ""},
	{"/tasks", "[stop <id>]", "background tasks; cancel one", "tools", ""},
	{"/worktrees", "[remove <b|all>]", "git worktrees kept by isolated subagents; delete one", "tools", ""},
	{"/config", "[key=value]", "settings: theme, verbose, tips, mouse, auto-compact, notifications, language, undo history, defaults", "other", ""},
	{"/vim", "", "switch vim editing of the prompt on or off", "other", ""},
	{"/debug", "[on|off]", "record this session's requests, responses and tool calls for review", "other", ""},
	{"/trace", "", "review the recorded trace in the browser: timeline, requests as sent, raw HTTP", "other", ""},
	{"/theme", "[auto|dark|light]", "pick the color theme, or follow the terminal (auto)", "other", ""},
	{"/keys", "", "keyboard shortcuts", "other", "?"},
	{"/info", "", "session sidebar: tasks, active MCP servers, language servers, skills used", "other", "f2"},
	{"/help", "", "commands and keys", "other", ""},
	{"/quit", "", "exit", "other", "ctrl+d"},
}

var helpText = func() string {
	var b strings.Builder
	b.WriteString("Commands\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "  %-30s %s\n", strings.TrimSpace(c.name+" "+c.args), c.desc)
	}
	fmt.Fprintf(&b, "  %-30s %s\n", "/<skill-name> [args]", "run a skill or a custom command (.claude/commands/<name>.md)")
	b.WriteString(`
Keys
  enter send · shift+enter / alt+enter / ctrl+j newline · esc interrupt
  @path attach a file · !cmd run a shell command · ↑/↓ history · ctrl+r search history · ctrl+g $EDITOR · ctrl+v paste image
  / command palette · ? shortcuts · F2 session info · shift+tab cycle permission mode · alt+p switch model · ctrl+o show thinking
  ctrl+c clear input / interrupt / quit`)
	return b.String()
}()

// paletteQuery returns the typed command name when the input is a bare
// "/name" being typed, which is when the palette shows.
func (m *model) paletteQuery() (string, bool) {
	v := m.input.Value()
	if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \n") || m.paletteHidden == v {
		return "", false
	}
	return v, true
}

// syncPalette rebuilds the palette rows for the current input.
func (m *model) syncPalette() {
	q, ok := m.paletteQuery()
	if !ok {
		m.palette = nil
		if !strings.HasPrefix(m.input.Value(), "/") {
			m.paletteHidden = ""
		}
		return
	}
	if m.palette != nil && m.paletteQ == q {
		return
	}
	m.paletteQ = q
	p := &picker{}
	var exact, prefix, other []pickItem
	add := func(name, args, desc, section, key string) {
		it := pickItem{section: section, label: name, detail: desc, note: key, value: command{name: name, args: args}}
		switch {
		case name == q:
			exact = append(exact, it)
		case strings.HasPrefix(name, q):
			prefix = append(prefix, it)
		case len(q) > 1 && strings.Contains(name, q[1:]):
			other = append(other, it)
		}
	}
	for _, c := range commands {
		add(c.name, c.args, c.desc, c.section, m.keys.remap(c.key))
	}
	if m.opts.Skills != nil {
		for _, sk := range m.opts.Skills.List() {
			if sk.UserInvocable {
				args, section := "[args]", "skills"
				if sk.ArgumentHint != "" {
					args = sk.ArgumentHint
				}
				if sk.Command {
					section = "custom commands"
				}
				if sk.Builtin {
					args, section = sk.ArgumentHint, "review"
				}
				add("/"+sk.Name, args, sk.Description, section, "")
			}
		}
	}
	if m.opts.MCP != nil {
		for _, p := range m.opts.MCP.Prompts() {
			add("/"+p.Command, p.ArgHint(), p.Description, "MCP prompts", "")
		}
	}
	// Exact and prefix matches first, each group keeping its section order.
	for _, group := range [][]pickItem{exact, prefix, other} {
		p.items = append(p.items, group...)
	}
	m.palette = p
	p.home()
}

// handlePaletteKey handles keys while the palette shows; ok is false for
// keys that belong to the input.
func (m *model) handlePaletteKey(msg tea.KeyPressMsg) (cmd tea.Cmd, ok bool) {
	p := m.palette
	sel, has := p.selected()
	switch msg.String() {
	case "up", "ctrl+p", "down", "ctrl+n", "pgup", "pgdown":
		p.handleKey(msg)
		return nil, true
	case "esc":
		m.paletteHidden = m.input.Value()
		m.palette = nil
		return nil, true
	case "tab":
		if has {
			m.complete(sel.value.(command))
		}
		return nil, true
	case "enter":
		if !has {
			return nil, false
		}
		c := sel.value.(command)
		if strings.HasPrefix(c.args, "<") { // needs an argument: complete instead
			m.complete(c)
			return nil, true
		}
		m.input.Reset()
		m.palette = nil
		return m.command(c.name), true
	}
	return nil, false
}

func (m *model) complete(c command) {
	v := c.name
	if c.args != "" {
		v += " "
	}
	m.input.SetValue(v)
	m.input.CursorEnd()
	m.palette = nil
}

func (m *model) paletteView() string {
	p := m.palette
	w := max(m.width-6, 20)
	p.height = max(min(12, m.availablePanelRows()-3), 1)
	hint := "↑/↓ move · tab complete · enter run · esc close"
	if len(p.visible()) == 0 {
		return m.st.box.Width(max(m.width-2, 10)).Render(m.st.dim.Render("no matching command · esc close"))
	}
	return m.st.modal.Width(max(m.width-2, 10)).Render(p.view(m.st, w) + "\n" + m.st.dim.Render(hint))
}

// modeLabels are the status-line names of the permission modes.
var modeLabels = map[permission.Mode]string{
	permission.ModeDefault:     "default",
	permission.ModeAcceptEdits: "⏵⏵ accept edits",
	permission.ModePlan:        "⏸ plan mode",
	permission.ModeAuto:        "✦ auto",
	permission.ModeYolo:        "⚠ yolo",
}

var modeChoices = []struct {
	mode permission.Mode
	desc string
}{
	{permission.ModeDefault, "Ask before edits and commands"},
	{permission.ModeAcceptEdits, "Edit files in this project freely, ask before commands"},
	{permission.ModePlan, "Read-only: explore and propose, change nothing"},
	{permission.ModeAuto, "Edit freely; a model approves safe actions and asks you about risky ones"},
	{permission.ModeYolo, "Run everything without asking"},
}

func (m *model) openModePicker() tea.Cmd {
	cur := m.agent.Perms().Mode()
	p := &picker{}
	for i, c := range modeChoices {
		it := pickItem{label: fmt.Sprintf("%d. %s", i+1, modeLabels[c.mode]), detail: c.desc, value: c.mode, warn: c.mode == permission.ModeYolo}
		if c.mode == cur {
			it.note, it.noteOK = "✓ current", true
		}
		p.items = append(p.items, it)
	}
	p.selectWhere(func(it pickItem) bool { return it.value == cur })
	m.modePick = p
	return nil
}

func (m *model) handleModePickerKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.modePick
	k := msg.String()
	switch {
	case k == "esc" || k == "ctrl+c":
		m.modePick = nil
		return nil
	case len(k) == 1 && k[0] >= '1' && k[0] <= byte('0'+len(modeChoices)):
		return m.setMode(modeChoices[k[0]-'1'].mode)
	case k == "shift+tab":
		p.move(1)
		return nil
	}
	if p.handleKey(msg) {
		it, _ := p.selected()
		return m.setMode(it.value.(permission.Mode))
	}
	return nil
}

// persistMode updates the saved default before applying it to this session.
func (m *model) persistMode(md permission.Mode) error {
	value := string(md)
	if md == permission.ModeDefault {
		value = ""
	}
	if err := m.opts.Config.SetUserSetting("mode", value); err != nil {
		return err
	}
	m.opts.Config.Mode = permission.Mode(value)
	m.agent.Perms().SetMode(md)
	return nil
}

func (m *model) setMode(md permission.Mode) tea.Cmd {
	if err := m.persistMode(md); err != nil {
		return m.println(m.st.err.Render("couldn't save mode: " + err.Error()))
	}
	m.modePick = nil
	return m.println(m.st.dim.Render("mode set to ") + m.st.modeStyle(md).UnsetPadding().Render(modeLabels[md]) + m.st.dim.Render(" · saved as default"))
}

func (m *model) modePickerView() string {
	w := max(m.width-6, 20)
	m.modePick.height = max(m.availablePanelRows()-4, 1)
	head := spread(m.st.accent.Render("Permission mode"), m.st.dim.Render("what larik may do without asking"), w)
	hint := "1–4 or ↑/↓ + enter select · esc close"
	return m.st.modal.Width(max(m.width-2, 10)).Render(head + "\n" + m.modePick.view(m.st, w) + "\n" + m.st.dim.Render(hint))
}
