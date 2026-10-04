package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

// browserCommand changes the browser tool list only by rebuilding the app in a
// fresh context. Never mutate the current agent's tools mid-conversation.
func (m *model) browserCommand(arg string) tea.Cmd {
	info := func(s string) tea.Cmd { return m.println(m.st.dim.Render(s)) }
	fail := func(s string) tea.Cmd { return m.println(m.st.err.Render(s)) }
	if arg == "" {
		switch {
		case !m.opts.Config.Browser.Enabled:
			return info("browser tools: off · /browser on to enable")
		case m.opts.App == nil:
			return info("browser tools: configured on · reload or start a new session to check availability")
		case m.opts.App.BrowserNote != "":
			return info(m.opts.App.BrowserNote)
		case m.opts.App.Browser == nil:
			return info("browser tools: unavailable (project settings may disable them)")
		default:
			return info("browser tools: on · /browser off to disable")
		}
	}
	if arg != "on" && arg != "off" {
		return fail("usage: /browser [on|off]")
	}
	if !m.idle() || m.agent.RunningBackground() > 0 || m.perm != nil {
		return fail("wait for the turn and background tasks before changing browser tools")
	}
	if m.opts.App == nil || m.sess == nil {
		return fail("browser toggle is unavailable for an externally managed session")
	}
	enabled := arg == "on"
	if m.opts.Config.Browser.Enabled == enabled {
		return m.browserCommand("")
	}
	apply := func() tea.Cmd {
		if err := m.opts.Config.SetBrowserEnabled(enabled); err != nil {
			return fail("couldn't save browser setting: " + err.Error())
		}
		old := m.opts.App
		cmd := m.reloadApp()
		if m.opts.App == old {
			return cmd // reload failed; its own error explains what happened
		}
		if enabled && m.opts.App != nil && m.opts.App.BrowserNote != "" {
			return tea.Batch(cmd, m.println(m.st.warn.Render(m.opts.App.BrowserNote)))
		}
		if enabled && !m.opts.Config.Browser.Enabled {
			return tea.Batch(cmd, m.println(m.st.warn.Render("browser tools are disabled by project settings")))
		}
		return cmd
	}
	if m.hasContext() {
		return m.askReload("Browser tools need a fresh context. Switch them "+strings.ToUpper(arg)+" and reload app services? (Transcript stays saved)", apply)
	}
	return apply()
}
