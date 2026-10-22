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
	switch arg {
	case "show":
		if m.opts.App == nil || m.opts.App.Browser == nil {
			return fail("browser tools are off · /browser on to enable them")
		}
		if m.browser == nil && m.panelAllowed() {
			return m.openBrowser()
		}
		return m.showBrowserPane()
	case "hide":
		if m.browser == nil {
			return info("the browser panel isn't open")
		}
		return tea.Batch(m.closeBrowser(), info("hid the browser · Chrome keeps the agent's pages · /browser quit closes it"))
	case "quit":
		if m.opts.App == nil || m.opts.App.Browser == nil {
			return fail("browser tools are off")
		}
		closed := m.closeBrowser()
		if !m.opts.App.Browser.Quit() {
			return tea.Batch(closed, info("the browser isn't running"))
		}
		return tea.Batch(closed, info("quit the browser · the agent's next browser call starts it again; sign-ins are kept"))
	}
	if arg != "on" && arg != "off" {
		return fail("usage: /browser [on|off|show|hide|quit]")
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
// showBrowserPane shows the browser where the panel can't go: in a split
// pane of the terminal's own, or by telling the user what to run. Starting
// Chrome and splitting take a moment, so it runs in the background.
func (m *model) showBrowserPane() tea.Cmd {
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
