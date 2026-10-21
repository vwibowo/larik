package tui

import (
	"context"
	"strings"
	"time"

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
			return info("browser tools: configured on · Chrome starts on first use")
		case m.opts.App.Browser == nil:
			return info("browser tools: unavailable (project settings may disable them)")
		case m.opts.App.BrowserNote != "":
			// The tools are on; the note says Chrome may not start.
			return tea.Batch(info("browser tools: on · /browser off to disable"), m.println(m.st.warn.Render(m.opts.App.BrowserNote)))
		default:
			return info("browser tools: on · /browser off to disable")
		}
	}
	if arg == "show" {
		return m.showBrowser(info, fail)
	}
	if arg != "on" && arg != "off" {
		return fail("usage: /browser [on|off|show]")
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
		// The reload finishes in the background; finishReload reports a
		// browser warning from the rebuilt app.
		return m.reloadApp()
	}
	if m.hasContext() {
		return m.askReload("Browser tools need a fresh context. Switch them "+strings.ToUpper(arg)+" and reload app services? (Transcript stays saved)", apply)
	}
	return apply()
}

// showBrowser opens the in-terminal browser pane. Starting Chrome and
// splitting the pane take a moment, so it runs in the background.
func (m *model) showBrowser(info, fail func(string) tea.Cmd) tea.Cmd {
	if m.opts.App == nil || m.opts.App.Browser == nil {
		return fail("browser tools are off · /browser on to enable them")
	}
	b := m.opts.App.Browser
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		msg, err := b.Show(ctx)
		if err != nil {
			return outputMsg(m.st.err.Render("browser: " + err.Error()))
		}
		return outputMsg(m.st.dim.Render(msg))
	}
}
