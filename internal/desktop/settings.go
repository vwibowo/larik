package desktop

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

type settingsPage struct{ id, title, group string }

var settingsPages = []settingsPage{
	{"window", "Window", "General"},
	{"appearance", "Appearance", "General"},
	{"terminal", "Terminal", "Terminal"},
	{"keyboard", "Keyboard", "Terminal"},
	{"agent", "Agent defaults", "Agent"},
	{"behavior", "Agent behavior", "Agent"},
	{"tools", "Tools", "Agent"},
	{"integrations", "Coding agents", "Agent"},
	{"advanced", "Advanced", "Advanced"},
}

type settingsItem struct{ id, page, title, description string }

var settingsItems = []settingsItem{
	{"sidebar", "window", "Show sidebar", "Show projects and sessions beside your panes."},
	{"chooser", "window", "Ask what to open in new panes", "Choose a Larik session, worktree, terminal, or browser when opening a pane."},
	{"appearance", "appearance", "Appearance", "Follow your system, or use light or dark colors for Larik."},
	{"accent", "appearance", "Accent color", "Use your system accent or choose a color for selection and focus."},
	{"pane-opacity", "appearance", "Pane background opacity", "Let the desktop show through terminal, Files, and Database pane backgrounds while text stays solid."},
	{"font", "terminal", "Terminal text size", "Size of text in every terminal pane."},
	{"model", "agent", "Default model", "Model used when you start a new Larik session."},
	{"effort", "agent", "Reasoning effort", "How much reasoning the default model uses when available."},
	{"mode", "agent", "Permission mode", "What a new session may do before asking you."},
	{"execution", "agent", "Execution style", "How the model calls tools and code scripts."},
	{"language", "behavior", "Response language", "Language for new replies; leave blank to let the model choose."},
	{"auto_compact", "behavior", "Compact long conversations", "Summarize a conversation when its context is nearly full."},
	{"token_saver", "behavior", "Shorten command output", "Filter recognized successful command output before sending it to the model."},
	{"prompt_suggestions", "behavior", "Prompt suggestions", "Suggest a next prompt after a turn; this uses tokens."},
	{"tool_search", "tools", "Load MCP tools on demand", "Keep large tool lists out of every model request."},
	{"integration-skills", "integrations", "Desktop control skills", "Install Larik Desktop skills globally for coding agents in every project."},
	{"integration-claude", "integrations", "Claude Code status", "Show work, permission requests, completion and failures from Claude Code terminal panes."},
	{"integration-codex", "integrations", "Codex CLI status", "Show work, permission requests and completion from Codex terminal panes."},
	{"integration-opencode", "integrations", "OpenCode status", "Show work, permission requests, completion and errors from OpenCode terminal panes."},
	{"integration-gemini", "integrations", "Gemini CLI status", "Show work, permission requests and completion from Gemini CLI terminal panes."},
	{"connect", "advanced", "Connect a provider", "Add a cloud, local, or custom model provider."},
	{"routing", "advanced", "Model routing", "Choose models for subagents, fallbacks, and budgets."},
	{"permissions", "advanced", "Permission rules", "Edit allow and deny rules for tools and commands."},
	{"mcp", "advanced", "MCP servers", "Connect and manage external tools."},
	{"hooks", "advanced", "Lifecycle hooks", "Configure trusted commands that run at lifecycle events."},
	{"sandbox", "advanced", "Sandbox", "Set command confinement and allowed access."},
	{"config", "advanced", "Open advanced settings…", "Open every Larik setting in the /config editor."},
}

func pageTitle(id string) string {
	for _, p := range settingsPages {
		if p.id == id {
			return p.title
		}
	}
	return "Window"
}

func (a *App) settingsMatch(item settingsItem, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	return q == "" || strings.Contains(strings.ToLower(pageTitle(item.page)+" "+item.title+" "+item.description+" "+a.settingsDescription(item.id, item.description)), q)
}

// settingsView presents desktop preferences and personal agent defaults.
func (a *App) settingsView(c *ui.Context) {
	if a.settings == nil {
		a.settings = &settingsState{page: "window", errors: map[string]string{}}
		a.refreshSettings()
	}
	t := a.palette(c.Theme())
	ui.Themed(c, t).Fill().Background(t.Background).Children(func() {
		ui.Row(c).Fill().Children(func() {
			a.settingsSidebar(c, t)
			ui.Box(c).Width(1).FillHeight().Background(t.Border)
			a.settingsContent(c, t)
		})
	})
}

func (a *App) settingsSidebar(c *ui.Context, t *ui.Theme) {
	s := a.settings
	ui.Column(c).Width(220).FillHeight().Background(sidebarColor(t)).Padding(0, 12, 16, 12).Gap(12).Children(func() {
		ui.Box(c).Height(max(c.TitleBar().Height, toolbarHeight)).FillWidth().DragWindow()
		ui.SearchField(c.Key("settings-search"), &s.query).Placeholder("Search settings").Label("Search settings").
			OnShortcut(0, ui.KeyEscape, func() { s.query = "" })
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).Gap(3).Children(func() {
				group := ""
				for _, p := range settingsPages {
					if group != p.group {
						group = p.group
						ui.Text(c, group).Bold().FontSize(11).TextColor(t.TextMuted).Padding(12, 8, 4, 8)
					}
					p := p
					fill := ui.Transparent
					if s.query == "" && s.page == p.id {
						fill = t.Selection
					}
					button := ui.ButtonBase(c.Key(p.id)).Label("Show "+p.title+" settings").Radius(8).Background(fill).Padding(8, 10).FillWidth().Justify(ui.Start).Children(func() {
						ui.Text(c, p.title).FontSize(13).TextColor(t.Text)
					})
					if button.Clicked() {
						s.page, s.query = p.id, ""
					}
				}
			})
		})
	})
}

func (a *App) settingsContent(c *ui.Context, t *ui.Theme) {
	s := a.settings
	ui.Column(c).Grow(1).FillHeight().Children(func() {
		ui.Scroll(c.Key("settings-content-" + s.page)).Fill().Children(func() {
			ui.Column(c).Padding(16, 28, 28, 28).Gap(22).Children(func() {
				if s.query == "" && (s.page == "agent" || s.page == "behavior" || s.page == "tools") {
					ui.Text(c, "Personal defaults for new sessions. Open sessions keep their current settings.").FontSize(12).TextColor(t.TextMuted)
				}
				if s.loadError != "" {
					ui.Text(c, "Could not load agent settings: "+s.loadError).TextColor(t.Danger)
				}
				found := false
				for _, p := range settingsPages {
					if s.query == "" && s.page != p.id {
						continue
					}
					var matches []settingsItem
					for _, item := range settingsItems {
						if item.page == p.id && a.settingsMatch(item, s.query) {
							matches = append(matches, item)
						}
					}
					keyboard := p.id == "keyboard" && a.keyboardMatches(s.query)
					if len(matches) == 0 && !keyboard {
						continue
					}
					found = true
					if s.query != "" {
						ui.Text(c, p.title).Bold().FontSize(15)
					}
					if keyboard {
						a.keySettingsFiltered(c, t, s.query)
					}
					if len(matches) > 0 {
						ui.Column(c).FillWidth().Border(1, t.Border).Radius(12).Children(func() {
							for i, item := range matches {
								if i > 0 {
									ui.Box(c).Height(1).Background(t.Border)
								}
								a.settingsRow(c, t, item)
							}
						})
					}
				}
				if !found {
					ui.Text(c, "No settings found").Bold().FontSize(15)
					ui.Text(c, "Try a setting name or what it does.").TextColor(t.TextMuted)
				}
			})
		})
	})
}

func (a *App) settingsRow(c *ui.Context, t *ui.Theme, item settingsItem) {
	ui.Row(c.Key(item.id)).FillWidth().Padding(15, 18).Gap(18).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Gap(4).Children(func() {
			ui.Text(c, item.title).Bold().FontSize(13)
			ui.Text(c, a.settingsDescription(item.id, item.description)).FontSize(12).TextColor(t.TextMuted)
			if note := a.settingsValueNote(item.id); note != "" {
				ui.Text(c, note).FontSize(11).TextColor(t.TextMuted)
			}
			if err := a.settings.errors[item.id]; err != "" {
				ui.Text(c, err).FontSize(11).TextColor(t.Danger)
			}
		})
		a.settingsControl(c, t, item.id)
	})
}

func (a *App) settingsControl(c *ui.Context, t *ui.Theme, id string) {
	switch id {
	case "sidebar", "chooser":
		on := !a.noSidebar
		if id == "chooser" {
			on = !a.noChooser
		}
		if ui.Switch(c.Key("switch"), &on).Label(id).Changed() {
			if id == "sidebar" {
				a.noSidebar = !on
			} else {
				a.noChooser = !on
			}
			a.settingsChanged()
		}
	case "appearance":
		i := max(0, indexOf(appearances, a.appearance))
		if ui.Segmented(c.Key("choice"), &i, "System", "Light", "Dark").Label("Appearance").Changed() {
			a.setAppearance(appearances[i])
		}
	case "accent":
		ui.Row(c.Key("colors")).Gap(5).Children(func() {
			swatch := func(key, name, hex string, fill ui.Color) {
				b := ui.ButtonBase(c.Key(key)).Size(19, 19).Radius(10).Background(fill).Label("Accent: " + name).Tooltip(name)
				if a.accent == hex {
					b.Border(2, t.Text)
				}
				if b.Clicked() {
					a.accent = hex
					a.settingsChanged()
				}
			}
			swatch("system", "System", "", t.TextMuted.Alpha(0.35))
			for _, ac := range accents {
				swatch(ac.name, ac.name, ac.hex, ui.Hex(ac.hex))
			}
		})
	case "font":
		v := float64(a.termFont().Size)
		ui.Row(c).Gap(9).AlignItems(ui.Center).Children(func() {
			if ui.Slider(c.Key("size"), &v, minFont, maxFont).Width(140).Label("Adjust terminal text size").Changed() {
				steps := int(math.Round(v)) - int(a.termFont().Size)
				if steps != 0 {
					a.zoom(terminal.Zoom{Steps: steps})
					a.settingsChanged()
				}
			}
			ui.Text(c, fmt.Sprintf("%g pt", a.termFont().Size)).MinWidth(40)
			reset := ui.Button(c.Key("reset"), "Reset").Label("Reset terminal text size")
			reset.Disabled(a.fontSize == 0)
			if reset.Clicked() {
				a.zoom(terminal.Zoom{Reset: true})
				a.settingsChanged()
			}
		})
	case "pane-opacity":
		v := float64(a.paneOpacity) * 100
		ui.Row(c).Gap(9).AlignItems(ui.Center).Children(func() {
			if ui.Slider(c.Key("opacity"), &v, 0, 100).Width(140).Label("Adjust pane background opacity").Changed() {
				a.paneOpacity = float32(math.Round(v) / 100)
				a.settingsChanged()
			}
			ui.Text(c, fmt.Sprintf("%d%%", int(math.Round(v)))).MinWidth(40)
			reset := ui.Button(c.Key("opacity-reset"), "Reset").Label("Reset pane background opacity")
			reset.Disabled(a.paneOpacity == 1)
			if reset.Clicked() {
				a.paneOpacity = 1
				a.settingsChanged()
			}
		})
	default:
		a.agentSettingControl(c, t, id)
	}
}

func (a *App) settingsChanged() {
	a.saveState()
	if a.redraw != nil {
		a.redraw()
	}
}
