package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/providers"
	"larik/internal/tools"
)

// settingsPanel is the /config screen: a list of settings, and for the one
// being changed, its values or a text field. Changes are saved to the user
// config (~/.config/larik/config.json) and applied to this session right
// away.
type settingsPanel struct {
	list    *picker
	editing string           // key of the setting being changed
	values  *picker          // its values, for a choice
	input   *textinput.Model // its text, for a text setting
	status  string           // result of the last change
	failed  bool
	guide   string             // key of a file-only setting whose instructions are open
	rules   *permissionsEditor // native allow/deny rule editor
	display *displayEditor     // status-line or sidebar command editor
	sandbox *sandboxEditor     // native sandbox access editor
	// themeWas restores the theme when a previewed choice is abandoned.
	themeWas string
}

type settingKind int

const (
	kindChoice settingKind = iota
	kindToggle
	kindText
	kindNumber // a whole number, within min and max
	kindGuide  // explains a setting that still needs the settings file
	kindAction // opens another screen
)

type settingChoice struct {
	value, label, desc string
	warn               bool
}

// applyKind is what has to happen before a saved setting is actually in
// force. Settings that shape the prompt or the tool list cannot be applied to
// a conversation already under way: the prefix sent to the model has to stay
// byte-identical within one context, so they wait for a fresh one.
type applyKind int

const (
	applyNow    applyKind = iota // the running session picks it up
	applyFresh                   // the prompt changes: clear the model context
	applyReload                  // services are built from it: rebuild the app
)

// settingSpec describes one row of /config and how to change it.
type settingSpec struct {
	key, title, section string
	// path is where the setting is written in the settings file, dotted for
	// one inside a section ("audio.auto_speak"). Empty means the key itself.
	path        string
	kind        settingKind
	choices     []settingChoice
	placeholder string // for text and number settings
	unit        string // what a number counts, e.g. "turns"
	min, max    int    // a number's bounds, both inclusive
	applies     applyKind
	later       string // said after saving when it doesn't apply at once
	// get returns the current value: a choice value, "on"/"off", or text.
	get func(m *model) string
	// store turns a checked value into what the config file holds; nil
	// removes the key so the built-in default applies.
	store func(v string) any
	// set applies a checked value to the config and this session.
	set func(m *model, v string)
	// other, for a choice, accepts a typed value that isn't one of the
	// choices (e.g. any number of days), returning it normalized.
	other func(v string) (string, bool)
	// guide explains why a setting is not editable here and shows its shape.
	guide string
	// For an action: the command that changes it, and how to open its screen.
	cmd  string
	open func(m *model) tea.Cmd
	// covers names the settings an action row is responsible for, when they
	// are not its key: /routing is one row over four of them.
	covers []string
}

// configPath is the dotted path the setting is written at.
func (s settingSpec) configPath() string {
	if s.path != "" {
		return s.path
	}
	return s.key
}

func toggle(key, title, section string, get func(c *config.Config) bool, set func(m *model, on bool)) settingSpec {
	return settingSpec{
		key: key, title: title, section: section, kind: kindToggle,
		get:   func(m *model) string { return onOff(get(m.opts.Config)) },
		store: func(v string) any { return v == "on" },
		set:   func(m *model, v string) { set(m, v == "on") },
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// guideSetting keeps a file-only option discoverable without pretending that
// typing its JSON representation is a native TUI editor.
func guideSetting(key, path, title, section, why, example string) settingSpec {
	return settingSpec{
		key: key, path: path, title: title, section: section, kind: kindGuide,
		guide: why, placeholder: example,
		get: func(*model) string { return "settings file" },
	}
}

// orEmpty stores v, or removes the key when v is the default.
func orEmpty(def string) func(string) any {
	return func(v string) any {
		if v == def {
			return nil
		}
		return v
	}
}

var settingSpecs = []settingSpec{
	{
		key: "theme", title: "Theme", section: "appearance", kind: kindChoice,
		choices: []settingChoice{
			{value: "auto", label: "Auto", desc: "follow the terminal's background"},
			{value: "dark", label: "Dark", desc: "colors for a dark background"},
			{value: "light", label: "Light", desc: "colors for a light background"},
		},
		get:   func(m *model) string { return m.theme() },
		store: orEmpty("auto"),
		set: func(m *model, v string) {
			if v == "auto" {
				v = ""
			}
			m.previewTheme(v)
		},
	},
	{
		key: "appearance", title: "Message appearance", section: "appearance", kind: kindChoice,
		choices: []settingChoice{
			{value: "compact", label: "Compact", desc: "group successful tool activity into a summary after each reply"},
			{value: "default", label: "Default", desc: "show tool activity as it happens"},
			{value: "verbose", label: "Verbose", desc: "show expanded tool output and thinking"},
		},
		get: func(m *model) string { return m.appearance }, store: orEmpty("default"),
		set: func(m *model, v string) {
			m.opts.Config.Appearance = v
			m.appearance = v
			m.verbose = v == "verbose"
			m.showThinking = m.verbose
		},
	},
	toggle("spinner_tips", "Spinner tips", "appearance",
		(*config.Config).TipsOn,
		func(m *model, on bool) {
			m.opts.Config.SpinnerTips = &on
			m.tips = on
		}),
	toggle("mouse", "Mouse scrolling", "appearance",
		(*config.Config).MouseOn,
		func(m *model, on bool) {
			m.opts.Config.Mouse = &on
			m.mouse = on
		}),
	{
		key: "audio_auto_speak", path: "audio.auto_speak", title: "Speak replies", section: "audio", kind: kindToggle,
		get:   func(m *model) string { return onOff(m.opts.Config.Audio.AutoSpeak) },
		store: func(v string) any { return v == "on" },
		set:   func(m *model, v string) { on := v == "on"; m.opts.Config.Audio.AutoSpeak = on },
	},
	{
		key: "editor_mode", title: "Editor mode", section: "appearance", kind: kindChoice,
		choices: []settingChoice{
			{value: "normal", label: "Normal", desc: "type and edit as usual"},
			{value: "vim", label: "Vim", desc: "esc for normal mode: hjkl, w b e, d c y, p, u, ."},
		},
		get: func(m *model) string {
			if m.vim != nil {
				return "vim"
			}
			return "normal"
		},
		store: orEmpty("normal"),
		set: func(m *model, v string) {
			m.opts.Config.EditorMode = v
			switch {
			case v == "vim" && m.vim == nil:
				m.vim = newVim()
			case v != "vim":
				m.vim = nil
			}
		},
	},
	toggle("auto_compact", "Auto-compact", "behavior",
		(*config.Config).AutoCompactOn,
		func(m *model, on bool) {
			m.opts.Config.AutoCompact = &on
			m.agent.SetAutoCompact(on)
		}),
	toggle("token_saver", "Token saver", "behavior",
		(*config.Config).TokenSaverOn,
		func(m *model, on bool) {
			m.opts.Config.TokenSaver = &on
			m.agent.SetTokenSaver(on)
		}),
	{
		key: "execution", title: "Default execution", section: "behavior", kind: kindChoice,
		choices: []settingChoice{
			{value: "tools", label: "Tool chain", desc: "one tool call per step"},
			{value: "hybrid", label: "Hybrid", desc: "tools, plus scripts that call them when a task has many steps"},
			{value: "code", label: "Code", desc: "ordinary tools via scripts; plan approval stays direct"},
		},
		applies: applyFresh,
		later:   "default for models without their own setting (/execution sets one)",
		get: func(m *model) string {
			e, _ := tools.ParseExecution(string(m.opts.Config.Execution))
			return string(e)
		},
		store: orEmpty("tools"),
		set: func(m *model, v string) {
			e, _ := tools.ParseExecution(v)
			m.opts.Config.Execution = e
			if e == tools.ExecTools {
				m.opts.Config.Execution = ""
			}
			m.agent.SetExecution(m.opts.Config.ExecutionFor(m.agent.ProviderName(), m.agent.Model()))
		},
	},
	{
		key: "notifications", title: "Notifications", section: "behavior", kind: kindChoice,
		choices: []settingChoice{
			{value: "off", label: "Off"},
			{value: "bell", label: "Terminal bell", desc: "when larik needs you and you're elsewhere"},
			{value: "desktop", label: "Desktop", desc: "system notification; the bell if unsupported"},
		},
		get:   func(m *model) string { return m.notify },
		store: orEmpty("off"),
		set: func(m *model, v string) {
			m.notify, m.opts.Config.Notifications = v, v
			if v == "off" {
				m.opts.Config.Notifications = ""
			}
		},
	},
	{
		key: "language", title: "Response language", section: "behavior", kind: kindText,
		placeholder: "e.g. Indonesian; empty lets the model choose",
		applies:     applyFresh,
		later:       "active in the fresh model context",
		get:         func(m *model) string { return m.opts.Config.Language },
		store:       orEmpty(""),
		set: func(m *model, v string) {
			m.opts.Config.Language = v
			m.agent.SetLanguage(v)
		},
	},
	{
		key: "checkpoint_retention_days", title: "Undo history", section: "behavior", kind: kindChoice, unit: "days",
		choices: []settingChoice{
			{value: "1", label: "1 day"},
			{value: "7", label: "7 days", desc: "the default"},
			{value: "30", label: "30 days"},
			{value: "90", label: "90 days"},
			{value: "forever", label: "Forever", desc: "never delete old snapshots"},
		},
		later: "old snapshots are deleted when larik starts",
		get: func(m *model) string {
			switch d := m.opts.Config.CheckpointRetentionDays; {
			case d < 0:
				return "forever"
			case d == 0:
				return "7"
			default:
				return strconv.Itoa(d)
			}
		},
		store: func(v string) any {
			switch v {
			case "7":
				return nil
			case "forever":
				return -1
			}
			n, _ := strconv.Atoi(v)
			return n
		},
		set: func(m *model, v string) {
			switch v {
			case "7":
				m.opts.Config.CheckpointRetentionDays = 0
			case "forever":
				m.opts.Config.CheckpointRetentionDays = -1
			default:
				m.opts.Config.CheckpointRetentionDays, _ = strconv.Atoi(v)
			}
		},
		other: func(v string) (string, bool) {
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(v, "days")), "d"))
			if err != nil || n < 1 {
				return "", false
			}
			return strconv.Itoa(n), true
		},
	},
	{
		key: "max_turns", title: "Turn limit", section: "behavior", kind: kindNumber,
		unit: "turns", min: 1, max: 10_000, applies: applyReload,
		placeholder: "how many model requests one prompt may take; empty for 200",
		get: func(m *model) string {
			if n := m.opts.Config.MaxTurns; n > 0 {
				return strconv.Itoa(n)
			}
			return "" // never set, so the built-in limit applies
		},
		store: func(v string) any {
			if v == "" || v == "200" { // the built-in limit needs no setting
				return nil
			}
			n, _ := strconv.Atoi(v)
			return n
		},
		// Cleared leaves the field at zero, which is how an unset limit is
		// written down; reloading reads the built-in 200 back in.
		set: func(m *model, v string) { m.opts.Config.MaxTurns, _ = strconv.Atoi(v) },
	},
	{
		key: "stall_timeout", title: "Stall timeout", section: "behavior", kind: kindChoice,
		unit: "seconds", applies: applyReload,
		choices: []settingChoice{
			{value: "default", label: "Automatic", desc: "300s, or 900s for a local server still loading a model"},
			{value: "60", label: "60 seconds"},
			{value: "300", label: "5 minutes"},
			{value: "900", label: "15 minutes"},
			{value: "forever", label: "Wait forever", desc: "never give up on a silent server"},
		},
		get: func(m *model) string {
			switch s := m.opts.Config.StallTimeout; {
			case s < 0:
				return "forever"
			case s == 0:
				return "default"
			default:
				return strconv.Itoa(s)
			}
		},
		store: func(v string) any {
			switch v {
			case "default":
				return nil
			case "forever":
				return -1
			}
			n, _ := strconv.Atoi(v)
			return n
		},
		set: func(m *model, v string) {
			switch v {
			case "default":
				m.opts.Config.StallTimeout = 0
			case "forever":
				m.opts.Config.StallTimeout = -1
			default:
				m.opts.Config.StallTimeout, _ = strconv.Atoi(v)
			}
		},
		other: func(v string) (string, bool) {
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(v), "s"))
			if err != nil || n < 1 {
				return "", false
			}
			return strconv.Itoa(n), true
		},
	},
	{
		key: "tool_search", title: "Load MCP tools on demand", section: "tools", kind: kindChoice,
		applies: applyReload,
		choices: []settingChoice{
			{value: "auto", label: "Automatic", desc: "on once there are many, so they stay out of every request"},
			{value: "on", label: "Always", desc: "the model searches for a tool before using it"},
			{value: "off", label: "Never", desc: "send every MCP tool with every request"},
		},
		get: func(m *model) string {
			if s := m.opts.Config.ToolSearch; s != "" {
				return s
			}
			return "auto"
		},
		store: orEmpty("auto"),
		set:   func(m *model, v string) { m.opts.Config.ToolSearch = v },
	},
	{
		key: "memory", path: "memory.enabled", title: "Memory", section: "tools", kind: kindToggle,
		applies: applyReload,
		get:     func(m *model) string { return onOff(m.opts.Config.MemoryOn()) },
		store:   func(v string) any { return v == "on" },
		set:     func(m *model, v string) { on := v == "on"; m.opts.Config.Memory.Enabled = &on },
	},
	{
		key: "web_fetch", path: "web.fetch_disabled", title: "Web fetch", section: "tools", kind: kindToggle,
		applies: applyReload,
		get:     func(m *model) string { return onOff(!m.opts.Config.Web.FetchDisabled) },
		// Stored the other way round: the setting names what is switched off,
		// so that a file saying nothing leaves fetching on.
		store: func(v string) any { return v == "off" },
		set:   func(m *model, v string) { m.opts.Config.Web.FetchDisabled = v == "off" },
	},
	{
		key: "browser", path: "browser.enabled", title: "Browser tools", section: "tools", kind: kindAction,
		get: func(m *model) string { return onOff(m.opts.Config.Browser.Enabled) },
		cmd: "/browser",
		// Selecting the row switches them, through the command that knows how
		// to rebuild the app and report what project settings allow.
		open: func(m *model) tea.Cmd {
			return m.browserCommand(onOff(!m.opts.Config.Browser.Enabled))
		},
	},
	{
		key: "browser_headless", path: "browser.headless", title: "Browser without a window", section: "tools", kind: kindToggle,
		applies: applyReload,
		get:     func(m *model) string { return onOff(m.opts.Config.Browser.Headless) },
		store:   func(v string) any { return v == "on" },
		set:     func(m *model, v string) { m.opts.Config.Browser.Headless = v == "on" },
	},
	{
		key: "browser_chrome_path", path: "browser.chrome_path", title: "Chrome to launch", section: "tools", kind: kindText,
		placeholder: "a path to Chrome or Chromium; empty finds it for you",
		applies:     applyReload,
		get:         func(m *model) string { return m.opts.Config.Browser.ChromePath },
		store:       orEmpty(""),
		set:         func(m *model, v string) { m.opts.Config.Browser.ChromePath = v },
	},
	{
		key: "audio_enabled", path: "audio.enabled", title: "Audio", section: "audio", kind: kindToggle,
		applies: applyReload,
		later:   "recording and speech need stt and tts endpoints as well",
		get:     func(m *model) string { return onOff(m.opts.Config.Audio.Enabled) },
		store:   func(v string) any { return v == "on" },
		set:     func(m *model, v string) { m.opts.Config.Audio.Enabled = v == "on" },
	},
	{
		key: "audio_record_command", path: "audio.record_command", title: "Recording command", section: "audio", kind: kindText,
		placeholder: "records to the file given as $1; empty uses the built-in recorder",
		applies:     applyReload,
		get:         func(m *model) string { return m.opts.Config.Audio.RecordCommand },
		store:       orEmpty(""),
		set:         func(m *model, v string) { m.opts.Config.Audio.RecordCommand = v },
	},
	{
		key: "audio_play_command", path: "audio.play_command", title: "Playback command", section: "audio", kind: kindText,
		placeholder: "plays the file given as $1; empty uses the built-in player",
		applies:     applyReload,
		get:         func(m *model) string { return m.opts.Config.Audio.PlayCommand },
		store:       orEmpty(""),
		set:         func(m *model, v string) { m.opts.Config.Audio.PlayCommand = v },
	},
	{
		key: "audio_max_duration_seconds", path: "audio.max_duration_seconds", title: "Recording limit", section: "audio", kind: kindNumber,
		unit: "seconds", min: 1, max: 3600, applies: applyReload,
		placeholder: "how long one recording may run; empty for the default",
		get: func(m *model) string {
			if s := m.opts.Config.Audio.MaxDurationSeconds; s > 0 {
				return strconv.Itoa(s)
			}
			return ""
		},
		store: func(v string) any {
			if v == "" {
				return nil
			}
			n, _ := strconv.Atoi(v)
			return n
		},
		set: func(m *model, v string) { m.opts.Config.Audio.MaxDurationSeconds, _ = strconv.Atoi(v) },
	},
	{
		key: "status_line", title: "Custom status line", section: "interface", kind: kindAction,
		get: func(m *model) string {
			if m.opts.Config.StatusLine == nil {
				return "not configured"
			}
			return m.opts.Config.StatusLine.Command
		},
		cmd: "/statusline", open: (*model).openStatusLineEditor,
	},
	{
		key: "sidebar", title: "Custom sidebar", section: "interface", kind: kindAction,
		get: func(m *model) string {
			if m.opts.Config.Sidebar == nil {
				return "not configured"
			}
			return m.opts.Config.Sidebar.Command
		},
		cmd: "/sidebar-config", open: (*model).openSidebarEditor,
	},
	guideSetting("keybindings", "keybindings", "Keybindings", "interface", "Key capture, conflicts, alternate keys, and unbinding need a dedicated editor.", `"keybindings": {"external_editor": "ctrl+e", "paste_image": []}`),
	guideSetting("models", "models", "Model catalog overrides", "models and providers", "Catalog entries have several typed limits and prices and need an add/edit/remove wizard.", `"models": {"model-id": {"provider": "openai", "context_window": 128000}}`),
	{
		key: "permissions", title: "Permission rules", section: "security", kind: kindAction,
		get: func(m *model) string { return permissionSummary(m.opts.Config.Permissions) },
		cmd: "/permissions", open: (*model).openPermissions,
		covers: []string{"permissions"},
	},
	guideSetting("mcp_servers", "mcp_servers", "MCP server definitions", "tools", "Servers need transport-specific forms and safe handling for environment values and credentials.", `"mcp_servers": {"name": {"command": "server", "args": []}}`),
	guideSetting("hooks", "hooks", "Lifecycle hooks", "tools", "Hooks need event, matcher, command, and prompt editors with an execution warning.", `"hooks": {"PostToolUse": [{"matcher": "edit", "hooks": [{"command": "gofmt -w $FILE"}]}]}`),
	guideSetting("web_search", "web.search", "Web search backend", "tools", "Search providers need provider-specific endpoint and credential fields.", `"web": {"search": {"provider": "brave", "api_key_env": "BRAVE_API_KEY"}}`),
	guideSetting("audio_stt", "audio.stt", "Speech-to-text endpoint", "audio", "Speech endpoints need URL, model, language, and masked credential fields.", `"audio": {"stt": {"base_url": "http://localhost:8000/v1", "model": "whisper-1"}}`),
	guideSetting("audio_tts", "audio.tts", "Text-to-speech endpoint", "audio", "Speech endpoints need URL, model, voice, and masked credential fields.", `"audio": {"tts": {"base_url": "http://localhost:8000/v1", "model": "tts-1", "voice": "alloy"}}`),
	{
		key: "sandbox", title: "Bash sandbox", section: "security", kind: kindAction,
		get: func(m *model) string {
			if m.opts.Sandbox == nil {
				return "off or unavailable"
			}
			return m.opts.Sandbox.Summary()
		},
		cmd: "/sandbox-config", open: (*model).openSandboxEditor,
		covers: []string{"sandbox"},
	},
	guideSetting("lsp", "lsp", "Language servers", "tools", "Servers need command, extension, root-marker, environment, and enable/disable editors.", `"lsp": {"name": {"command": ["server", "--stdio"], "extensions": [".ext"]}}`),
	{
		key: "debug", title: "Record this session", section: "advanced", kind: kindToggle,
		later: "traces hold your prompts and file contents; /trace reviews them",
		get: func(m *model) string {
			if m.sess != nil {
				return onOff(m.sess.Trace() != nil)
			}
			return onOff(m.opts.Config.DebugOn())
		},
		store: func(v string) any { return v == "on" },
		set: func(m *model, v string) {
			on := v == "on"
			m.opts.Config.Debug = &on
			_, _ = m.setRecording(on)
		},
	},
	{
		key: "debug_retention_days", title: "Trace history", section: "advanced", kind: kindChoice,
		unit:  "days",
		later: "old traces are deleted when larik starts",
		choices: []settingChoice{
			{value: "1", label: "1 day"},
			{value: "14", label: "14 days", desc: "the default"},
			{value: "90", label: "90 days"},
			{value: "forever", label: "Forever", desc: "never delete old traces"},
		},
		get: func(m *model) string {
			switch d := m.opts.Config.DebugRetentionDays; {
			case d < 0:
				return "forever"
			case d == 0:
				return "14"
			default:
				return strconv.Itoa(d)
			}
		},
		store: func(v string) any {
			switch v {
			case "14":
				return nil
			case "forever":
				return -1
			}
			n, _ := strconv.Atoi(v)
			return n
		},
		set: func(m *model, v string) {
			switch v {
			case "14":
				m.opts.Config.DebugRetentionDays = 0
			case "forever":
				m.opts.Config.DebugRetentionDays = -1
			default:
				m.opts.Config.DebugRetentionDays, _ = strconv.Atoi(v)
			}
		},
		other: func(v string) (string, bool) {
			n, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(v, "days")), "d"))
			if err != nil || n < 1 {
				return "", false
			}
			return strconv.Itoa(n), true
		},
	},
	{
		key: "auto_mode_model", path: "auto_mode.model", title: "Auto-mode judge", section: "advanced", kind: kindText,
		placeholder: "provider/model or a role; empty uses this session's model",
		later:       "the model that approves safe actions in auto mode",
		get:         func(m *model) string { return m.opts.Config.AutoMode.Model },
		store:       orEmpty(""),
		set:         func(m *model, v string) { m.opts.Config.AutoMode.Model = v },
	},
	{
		key: "mode", title: "Permission mode", section: "defaults", kind: kindChoice,
		choices: []settingChoice{
			{value: "default", label: modeLabels[permission.ModeDefault], desc: "ask before edits and commands"},
			{value: "accept-edits", label: modeLabels[permission.ModeAcceptEdits], desc: "edit project files freely, ask before commands"},
			{value: "plan", label: modeLabels[permission.ModePlan], desc: "read-only: explore and propose"},
			{value: "auto", label: modeLabels[permission.ModeAuto], desc: "edit freely; a model approves safe actions, risky ones ask"},
			{value: "yolo", label: modeLabels[permission.ModeYolo], desc: "run everything without asking", warn: true},
		},
		get: func(m *model) string {
			if md := m.opts.Config.Mode; md != "" {
				return string(md)
			}
			return "default"
		},
		store: orEmpty("default"),
		set: func(m *model, v string) {
			md, _ := permission.ParseMode(v)
			m.opts.Config.Mode = md
			if v == "default" {
				m.opts.Config.Mode = ""
			}
			m.agent.Perms().SetMode(md)
		},
	},
	{
		key: "effort", title: "Reasoning effort", section: "defaults", kind: kindChoice,
		choices: []settingChoice{
			{value: "default", label: "Provider default", desc: "let the model decide"},
			{value: "low", label: "Low"},
			{value: "medium", label: "Medium"},
			{value: "high", label: "High"},
			{value: "xhigh", label: "Extra high"},
			{value: "max", label: "Max"},
		},
		get: func(m *model) string {
			if e := m.opts.Config.Effort; e != "" {
				return string(e)
			}
			return "default"
		},
		store: orEmpty("default"),
		set: func(m *model, v string) {
			e := llm.Effort(v)
			if v == "default" {
				e = llm.EffortDefault
			}
			m.opts.Config.Effort = e
			m.agent.SetEffort(e)
		},
	},
	{
		key: "model", title: "Model", section: "defaults", kind: kindAction,
		get:  func(m *model) string { return m.opts.Config.Model },
		cmd:  "/model",
		open: func(m *model) tea.Cmd { return m.openModelPicker() },
	},
	{
		key: "routing", title: "Model routing", section: "defaults", kind: kindAction,
		get: func(m *model) string {
			var set []string
			r := m.opts.Config.Routing()
			for _, n := range providers.RoleNames(m.opts.Config) {
				if spec := r.Roles[n]; spec != "" {
					set = append(set, n+" "+spec)
				}
			}
			return strings.Join(set, ", ")
		},
		cmd:    "/routing",
		open:   func(m *model) tea.Cmd { return m.openRouting(rtPreset) },
		covers: []string{"roles", "fallbacks", "role_options", "delegation"},
	},
	{
		key: "budget", title: "Session budget", section: "defaults", kind: kindAction,
		get: func(m *model) string {
			if b := m.opts.Config.Routing().Budget; b.SessionUSD > 0 {
				return fmt.Sprintf("$%.2f", b.SessionUSD)
			}
			return ""
		},
		cmd:    "/routing budget=",
		open:   func(m *model) tea.Cmd { return m.openRouting(rtBudget) },
		covers: []string{"budget"},
	},
}

func settingByKey(key string) (settingSpec, bool) {
	key = strings.ReplaceAll(strings.ToLower(key), "-", "_")
	for _, s := range settingSpecs {
		if s.key == key {
			return s, true
		}
	}
	return settingSpec{}, false
}

func (s settingSpec) label(v string) string {
	for _, c := range s.choices {
		if c.value == v {
			return c.label
		}
	}
	if s.other != nil {
		if n, ok := s.other(v); ok {
			return strings.TrimSpace(n + " " + s.unit)
		}
	}
	if s.kind == kindNumber {
		if v == "" {
			return "default"
		}
		return strings.TrimSpace(v + " " + s.unit)
	}
	return v
}

// check validates a value typed for s and normalizes it.
func (s settingSpec) check(v string) (string, error) {
	v = strings.TrimSpace(v)
	switch s.kind {
	case kindToggle:
		switch strings.ToLower(v) {
		case "on", "true", "yes", "1":
			return "on", nil
		case "off", "false", "no", "0":
			return "off", nil
		}
		return "", fmt.Errorf("%s is on or off", s.key)
	case kindChoice:
		var names []string
		for _, c := range s.choices {
			if strings.EqualFold(v, c.value) {
				return c.value, nil
			}
			names = append(names, c.value)
		}
		if s.other != nil {
			if n, ok := s.other(v); ok {
				return n, nil
			}
			return "", fmt.Errorf("%s is a number of %s, or forever", s.key, s.unit)
		}
		return "", fmt.Errorf("%s is one of %s", s.key, strings.Join(names, ", "))
	case kindNumber:
		// Empty leaves the setting out of the file, so Larik's own default
		// applies; "default" is the same thing said out loud.
		if v == "" || strings.EqualFold(v, "default") {
			return "", nil
		}
		n, err := strconv.Atoi(strings.TrimPrefix(v, "+"))
		if err != nil {
			return "", fmt.Errorf("%s is a whole number of %s, or default", s.key, s.unit)
		}
		if n < s.min || n > s.max {
			return "", fmt.Errorf("%s is between %d and %d %s", s.key, s.min, s.max, s.unit)
		}
		return strconv.Itoa(n), nil
	case kindGuide:
		return "", fmt.Errorf("%s is not editable in the TUI yet; open its /config guide", s.key)
	case kindAction:
		return "", fmt.Errorf("use %s to change the %s", s.cmd, s.key)
	}
	return v, nil
}

// busySetting reports why a setting cannot change right now, or "". One that
// rebuilds the prompt or the services has to wait for the work using them.
func (m *model) busySetting(spec settingSpec, value string) string {
	if spec.applies == applyNow || !m.settingChanges(spec, value) {
		return ""
	}
	if !m.idle() || m.agent.RunningBackground() > 0 {
		return spec.key + " can't change while a turn or task is running"
	}
	return ""
}

// saveSetting stores a setting in the user config and applies it to this
// session, returning the setting and what to tell the user. Putting it in
// force is the caller's: it may have to ask first.
func (m *model) saveSetting(key, raw string) (settingSpec, string, error) {
	spec, ok := settingByKey(key)
	if !ok {
		var keys []string
		for _, s := range settingSpecs {
			keys = append(keys, s.key)
		}
		return spec, "", fmt.Errorf("unknown setting %q (settings: %s)", key, strings.Join(keys, ", "))
	}
	v, err := spec.check(raw)
	if err != nil {
		return spec, "", err
	}
	if busy := m.busySetting(spec, v); busy != "" {
		return spec, "", fmt.Errorf("%s", busy)
	}
	if err := m.opts.Config.SetUserSettingPath(spec.configPath(), spec.store(v)); err != nil {
		return spec, "", fmt.Errorf("couldn't save: %w", err)
	}
	spec.set(m, v)
	shown := spec.label(v)
	if spec.kind == kindText && v == "" {
		shown = "not set"
	}
	return spec, spec.title + " set to " + shown + " · saved to " + shortHome(m.opts.Config.UserConfigPath()), nil
}

// configCommand handles /config key=value (or "key value").
func (m *model) configCommand(arg string) tea.Cmd {
	arg = strings.TrimSpace(arg)
	if !strings.ContainsAny(arg, "= ") {
		if spec, ok := settingByKey(arg); ok {
			switch spec.kind {
			case kindGuide:
				return m.openSettings(spec.key)
			case kindAction:
				return spec.open(m)
			}
		}
	}
	key, val, ok := strings.Cut(arg, "=")
	if !ok {
		key, val, _ = strings.Cut(arg, " ")
	}
	key = strings.TrimSpace(key)
	if spec, ok := settingByKey(key); ok {
		if v, err := spec.check(val); err == nil {
			if busy := m.busySetting(spec, v); busy != "" {
				return m.println(m.st.err.Render(busy))
			}
			if m.requiresFreshContext(spec, v) {
				return m.askReload(freshContextQuestion(spec), func() tea.Cmd { return m.configCommandApply(key, val) })
			}
		}
	}
	return m.configCommandApply(key, val)
}

// freshContextQuestion asks before a setting discards the conversation the
// model is holding, naming what it costs and what it does not.
func freshContextQuestion(spec settingSpec) string {
	what := "a fresh context"
	if spec.applies == applyReload {
		what = "a fresh context and rebuilt services"
	}
	return spec.title + " needs " + what + ". Clear the current model context and apply it? (Transcript stays saved)"
}

// applySaved saves a setting and puts it in force, returning what to tell
// the user and anything still to run.
func (m *model) applySaved(key, value string) (settingSpec, string, tea.Cmd, error) {
	// Whether this is a change has to be read before saving, which is what
	// makes the old value the new one.
	changes := false
	if spec, ok := settingByKey(key); ok {
		if v, err := spec.check(value); err == nil {
			changes = m.settingChanges(spec, v)
		}
	}
	spec, msg, err := m.saveSetting(key, value)
	if err != nil {
		return spec, "", nil, err
	}
	var cmd tea.Cmd
	applied := true
	if changes {
		cmd, applied = m.applySetting(spec)
	}
	if note := m.settingNote(spec, applied); note != "" {
		msg += " · " + note
	}
	return spec, msg, cmd, nil
}

func (m *model) configCommandApply(key, val string) tea.Cmd {
	_, msg, cmd, err := m.applySaved(key, val)
	if err != nil {
		return m.println(m.st.err.Render(err.Error()))
	}
	// The message goes out alongside a rebuild, never before it: rebuilding
	// resets the conversation, which would take the message with it.
	return tea.Batch(cmd, m.println(m.st.dim.Render(msg)))
}

// openSettings shows the /config screen, jumping straight to one
// setting's values when key is set.
func (m *model) openSettings(key string) tea.Cmd {
	m.settings = &settingsPanel{}
	m.buildSettings()
	if key != "" {
		m.settings.list.selectWhere(func(it pickItem) bool { return it.value == key })
		return m.editSetting(key)
	}
	return nil
}

func (m *model) buildSettings() {
	cfg := m.opts.Config
	// There are more settings than fit on a screen, so the list is typed at
	// rather than scrolled through. The filter reads the value as well as
	// the name: typing "vim" finds the editor mode by what it is set to.
	p := &picker{filterable: true, matchDetail: true}
	for _, spec := range settingSpecs {
		v := spec.get(m)
		it := pickItem{section: spec.section, label: spec.title, value: spec.key}
		switch spec.kind {
		case kindToggle:
			it.detail = v
		case kindText:
			it.detail = v
			if v == "" {
				it.detail = "not set"
			}
		case kindGuide:
			it.detail = "not editable in the TUI yet"
			it.note = "guide"
		case kindAction:
			it.detail = v
			if v == "" {
				it.detail = "not set"
			}
			it.note = spec.cmd
		default:
			it.detail = spec.label(v)
		}
		if spec.key == "theme" && v == "auto" {
			bg := "dark"
			if !m.termDark {
				bg = "light"
			}
			it.detail += " (" + bg + " terminal)"
		}
		p.items = append(p.items, it)
	}

	files := []struct{ path, label, what string }{
		{cfg.UserConfigPath(), shortHome(cfg.UserConfigPath()), "yours, all projects · /config saves here"},
		{filepath.Join(cfg.Cwd, ".larik", "settings.json"), ".larik/settings.json", "this project, shared"},
		{config.LocalSettingsPath(cfg.Cwd), "private project settings", "this project, yours"},
	}
	for _, f := range files {
		note := "not created"
		if _, err := os.Stat(f.path); err == nil {
			note = "exists"
		}
		p.items = append(p.items, pickItem{section: "files (later override earlier)", label: f.label, detail: f.what, note: note, disabled: true})
	}
	p.home()
	if old := m.settings.list; old != nil {
		p.filter = old.filter
		p.home()
		if it, ok := old.selected(); ok {
			p.selectWhere(func(n pickItem) bool { return n.value == it.value })
		}
	}
	m.settings.list = p
}

func shortHome(path string) string {
	if home, err := os.UserHomeDir(); err == nil {
		if rel, err := filepath.Rel(home, path); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
			return filepath.Join("~", rel)
		}
	}
	return path
}

// editSetting starts changing a setting: a toggle flips at once, a
// choice shows its values, text opens a field.
func (m *model) editSetting(key string) tea.Cmd {
	s := m.settings
	spec, _ := settingByKey(key)
	cur := spec.get(m)
	switch spec.kind {
	case kindAction:
		m.settings = nil
		if m.running && spec.key == "model" {
			return m.println(m.st.err.Render("the model can't change while a turn is running" + m.interruptHint()))
		}
		return spec.open(m)
	case kindToggle:
		next := "on"
		if cur == "on" {
			next = "off"
		}
		return m.confirmSetting(key, next)
	case kindGuide:
		s.guide = key
		return nil
	case kindText, kindNumber:
		ti := textinput.New()
		ti.Prompt = "› "
		ti.Placeholder = spec.placeholder
		ti.SetValue(cur)
		ti.SetWidth(max(m.width-10, 20))
		s.editing, s.input = key, &ti
		return ti.Focus()
	}
	p := &picker{}
	for _, c := range spec.choices {
		it := pickItem{label: c.label, detail: c.desc, value: c.value, warn: c.warn}
		if c.value == cur {
			it.note, it.noteOK = "✓ current", true
		}
		p.items = append(p.items, it)
	}
	p.selectWhere(func(it pickItem) bool { return it.value == cur })
	s.editing, s.values, s.themeWas = key, p, m.opts.Config.Theme
	return nil
}

// finishEdit saves a value and goes back to the list.
func (m *model) finishEdit(key, value string) tea.Cmd {
	s := m.settings
	if key == "theme" {
		m.previewTheme(s.themeWas) // so a failed save leaves the old theme
	}
	spec, msg, cmd, err := m.applySaved(key, value)
	s.editing, s.values, s.input = "", nil, nil
	if err != nil {
		s.status, s.failed = err.Error(), true
		m.buildSettings()
		return nil
	}
	if cmd != nil && spec.applies == applyReload {
		// Rebuilding replaces the settings this panel was drawn from, so it
		// closes and the result is reported in the conversation instead.
		m.settings = nil
		return tea.Batch(cmd, m.println(m.st.dim.Render(msg)))
	}
	s.status, s.failed = msg, false
	m.buildSettings()
	return cmd
}

func (m *model) handleSettingsKey(msg tea.KeyPressMsg) tea.Cmd {
	s := m.settings
	if s.rules != nil {
		return m.handlePermissionsKey(msg)
	}
	if s.display != nil {
		return m.handleDisplayKey(msg)
	}
	if s.sandbox != nil {
		return m.handleSandboxEditorKey(msg)
	}
	k := msg.String()
	switch {
	case s.guide != "":
		if k == "esc" || k == "ctrl+c" || k == "enter" {
			s.guide = ""
		}
		return nil

	case s.input != nil:
		switch k {
		case "esc", "ctrl+c":
			s.editing, s.input = "", nil
			return nil
		case "enter":
			return m.confirmSetting(s.editing, s.input.Value())
		}
		ti, cmd := s.input.Update(msg)
		s.input = &ti
		return cmd

	case s.values != nil:
		switch k {
		case "esc", "ctrl+c":
			if s.editing == "theme" {
				m.previewTheme(s.themeWas)
			}
			s.editing, s.values = "", nil
			return nil
		}
		if s.values.handleKey(msg) {
			it, _ := s.values.selected()
			return m.confirmSetting(s.editing, it.value.(string))
		}
		if s.editing == "theme" { // show the highlighted theme as the cursor moves
			if it, ok := s.values.selected(); ok {
				m.previewTheme(it.value.(string))
			}
		}
		return nil
	}

	switch k {
	case "ctrl+c":
		m.settings = nil
		return nil
	case "esc":
		// Esc backs out one step at a time: first whatever was typed to
		// narrow the list, then the panel.
		if s.list.filter != "" {
			s.list.filter = ""
			s.list.home()
			return nil
		}
		m.settings = nil
		return nil
	}
	if s.list.handleKey(msg) {
		it, _ := s.list.selected()
		return m.editSetting(it.value.(string))
	}
	return nil
}

// previewTheme applies a theme without saving it; "" means auto.
func (m *model) previewTheme(theme string) {
	if theme == "auto" {
		theme = ""
	}
	m.opts.Config.Theme = theme
	if d := m.wantDark(); d != m.isDark {
		m.applyTheme(d)
	}
}

// setTheme handles /theme <name>.
func (m *model) setTheme(name string) tea.Cmd {
	return m.configCommand("theme=" + name)
}

func (m *model) settingsView() string {
	s := m.settings
	if s.rules != nil {
		return m.permissionsView()
	}
	if s.display != nil {
		return m.displayEditorView()
	}
	if s.sandbox != nil {
		return m.sandboxEditorView()
	}
	w := max(m.width-6, 20)
	rows := m.availablePanelRows()
	var head, body, hint string
	switch {
	case s.guide != "":
		spec, _ := settingByKey(s.guide)
		head = spread(m.st.accent.Render(spec.title), m.st.dim.Render("configuration guide"), w)
		body = wrap("This setting is not editable in the TUI yet. "+spec.guide, w) + "\n\n" + m.st.dim.Render("Personal settings file") + "\n" +
			shortHome(m.opts.Config.UserConfigPath()) + "\n\n" + m.st.dim.Render("Example (inside the top-level JSON object)") + "\n" +
			wrap(spec.placeholder, w) + "\n\n" + m.st.dim.Render("Save the file, then run /reload. Project settings may have stricter trust rules.")
		hint = "enter/esc back"
	case s.input != nil:
		spec, _ := settingByKey(s.editing)
		head = spread(m.st.accent.Render(spec.title), m.st.dim.Render("saved for every project"), w)
		body = s.input.View()
		hint = "enter save · esc back"
	case s.values != nil:
		s.values.height = max(rows-4-boolRows(s.status != ""), 1)
		spec, _ := settingByKey(s.editing)
		head = spread(m.st.accent.Render(spec.title), m.st.dim.Render("saved for every project"), w)
		body = s.values.view(m.st, w)
		hint = "↑/↓ + enter choose · esc back"
	default:
		s.list.height = max(rows-5-boolRows(s.status != ""), 1)
		head = spread(m.st.accent.Render("Settings"), m.st.dim.Render("/config"), w)
		body = s.list.filterLine(m.st, "type to find a setting…") + "\n" + s.list.view(m.st, w)
		hint = "↑/↓ move · type to filter · enter change · esc close"
	}
	out := head + "\n" + body
	if s.status != "" {
		st := m.st.ok
		if s.failed {
			st = m.st.err
		}
		out += "\n" + st.Render(s.status)
	}
	return m.st.modal.Width(max(m.width-2, 10)).Render(out + "\n" + m.st.dim.Render(hint))
}

func boolRows(on bool) int {
	if on {
		return 1
	}
	return 0
}

// executionCommand handles /execution: with no argument it shows the
// current model's setting; a value saves it for that model ("default"
// removes the model's own setting, so the default applies).
func (m *model) executionCommand(arg string) tea.Cmd {
	cfg := m.opts.Config
	provider, modelID := m.agent.ProviderName(), m.agent.Model()
	key := provider + "/" + modelID
	describe := func() string {
		e, src := cfg.ExecutionSource(provider, modelID)
		from := "the default"
		if src != "" {
			from = "model_execution[" + src + "]"
		}
		return fmt.Sprintf("execution for %s: %s (from %s)", key, e, from)
	}
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return m.openExecPicker()
	}
	var e tools.Execution
	if arg != "default" {
		var err error
		if e, err = tools.ParseExecution(arg); err != nil {
			return m.println(m.st.err.Render(err.Error() + `, or "default"`))
		}
	}
	if !m.idle() || m.agent.RunningBackground() > 0 {
		return m.println(m.st.err.Render("execution can't change while a turn or task is running"))
	}
	_, source := cfg.ExecutionSource(provider, modelID)
	changesContext := arg != "default" && e != m.agent.Execution() || arg == "default" && source == key
	if changesContext && m.hasContext() && !m.reloadApproved {
		return m.askReload("Execution needs a fresh context. Clear the current model context and apply it? (Transcript stays saved)", func() tea.Cmd {
			m.reloadApproved = true
			defer func() { m.reloadApproved = false }()
			return m.executionCommand(arg)
		})
	}
	if err := cfg.SetModelExecution(key, e); err != nil {
		return m.println(m.st.err.Render("couldn't save execution: " + err.Error()))
	}
	m.agent.SetExecution(cfg.ExecutionFor(provider, modelID))
	if m.reloadApproved {
		m.clearForSetting()
	}
	when := "active now"
	if m.agent.Execution() != cfg.ExecutionFor(provider, modelID) {
		when = "applies after /clear or in a new session"
	}
	return m.println(m.st.dim.Render(describe() + " · saved to " + shortHome(cfg.UserConfigPath()) + " · " + when))
}

// execChoices are the /execution picker's rows; "default" removes the
// model's own setting.
var execChoices = []struct{ value, label, desc string }{
	{"tools", "Tool chain", "one tool call per step"},
	{"hybrid", "Hybrid", "tools, plus scripts that call them when a task has many steps"},
	{"code", "Code", "ordinary tools via scripts; plan approval stays direct"},
	{"default", "Default", "drop this model's own setting and use the default"},
}

func (m *model) openExecPicker() tea.Cmd {
	cfg := m.opts.Config
	saved, src := cfg.ExecutionSource(m.agent.ProviderName(), m.agent.Model())
	def, _ := tools.ParseExecution(string(cfg.Execution))
	cur := "default"
	if src != "" {
		cur = string(saved)
	}
	p := &picker{}
	for i, c := range execChoices {
		it := pickItem{label: fmt.Sprintf("%d. %s", i+1, c.label), detail: c.desc, value: c.value}
		if c.value == "default" {
			it.detail = fmt.Sprintf("use the default (%s) for this model", def)
			if src != "" && src != m.agent.ProviderName()+"/"+m.agent.Model() {
				it.detail += "; model_execution[" + src + "] still applies"
			}
		}
		if c.value == cur {
			it.note, it.noteOK = "✓ saved", true
		}
		p.items = append(p.items, it)
	}
	p.selectWhere(func(it pickItem) bool { return it.value == cur })
	m.execPick = p
	return nil
}

func (m *model) handleExecPickerKey(msg tea.KeyPressMsg) tea.Cmd {
	p := m.execPick
	k := msg.String()
	switch {
	case k == "esc" || k == "ctrl+c":
		m.execPick = nil
		return nil
	case len(k) == 1 && k[0] >= '1' && k[0] <= byte('0'+len(execChoices)):
		m.execPick = nil
		return m.executionCommand(execChoices[k[0]-'1'].value)
	}
	if p.handleKey(msg) {
		it, _ := p.selected()
		m.execPick = nil
		return m.executionCommand(it.value.(string))
	}
	return nil
}

func (m *model) execPickerView() string {
	w := max(m.width-6, 20)
	m.execPick.height = max(m.availablePanelRows()-4, 1)
	head := spread(m.st.accent.Render("Execution for "+m.agent.ProviderName()+"/"+m.agent.Model()), m.st.dim.Render("saved for this model"), w)
	hint := fmt.Sprintf("1–%d or ↑/↓ + enter select · esc close", len(execChoices))
	return m.st.modal.Width(max(m.width-2, 10)).Render(head + "\n" + m.execPick.view(m.st, w) + "\n" + m.st.dim.Render(hint))
}
