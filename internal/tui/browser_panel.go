package tui

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"larik/internal/termbrowser"
)

// The browser panel shows the agent's browser tab beside the conversation,
// in Larik's own window, in any terminal that shows kitty graphics. It
// takes the sidebar's column (the two are never shown at once). F3 opens
// it and moves the keyboard between it and the composer; clicking either
// side gives that side the keyboard; ctrl+q in the browser hides it.
// Chrome keeps running until /browser quit or Larik exits.

const (
	// browserMinWidth is the narrowest the panel gets, and the conversation
	// keeps at least browserMinConversation columns beside it.
	browserMinWidth        = 40
	browserMinConversation = 30
)

// browserRect is where the panel sits, in screen cells.
type browserRect struct{ x, y, cols, rows int }

type (
	openBrowserMsg struct{}
	// browserMovedMsg says View put the panel somewhere new (a resize, the
	// composer growing, a picker opening).
	browserMovedMsg struct{}
)

// browserPanelWidth is the panel's width at the current window size, or 0
// when the window is too narrow to show it beside the conversation.
func (m *model) browserPanelWidth() int {
	if m.browser == nil {
		return 0
	}
	pct := 50
	if c := m.opts.Config; c != nil && c.Browser.PaneSize > 0 {
		pct = c.Browser.PaneSize
	}
	if m.browserPct > 0 {
		pct = m.browserPct
	}
	w := max(m.width*pct/100, browserMinWidth)
	if m.width-w-1 < browserMinConversation {
		return 0
	}
	return w
}

// browserColumn is the panel's part of the screen: its two header rows,
// then blank cells the page's image is drawn over.
func (m *model) browserColumn(width, height int) string {
	m.browser.SetFocus(m.browserFocused, m.keys.hint(actToggleBrowser))
	h := m.browser.Header()
	lines := []string{h[0], h[1]}
	blank := strings.Repeat(" ", width)
	for len(lines) < height {
		lines = append(lines, blank)
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

// panelAllowed says whether the browser can open as a panel here.
func (m *model) panelAllowed() bool { return m.panelAllowedAt(m.width) }

// openBrowser opens the panel, starting Chrome if it isn't running.
func (m *model) openBrowser() tea.Cmd {
	if m.browser != nil {
		m.browserFocused = true
		return nil
	}
	b := m.opts.App.Browser
	path, err := b.ServeDefault()
	if err != nil {
		return m.println(m.st.err.Render("browser: " + err.Error()))
	}
	p, err := termbrowser.NewPanel(path, m.send)
	if err != nil {
		return m.println(m.st.err.Render("browser: " + err.Error()))
	}
	m.browser, m.browserFocused, m.browserLaid = p, true, browserRect{}
	m.showInfo = false // the panel takes the sidebar's column
	start := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := b.Open(ctx); err != nil {
			return outputMsg(m.st.err.Render("browser: " + err.Error()))
		}
		return nil
	}
	return tea.Batch(p.Init(), start)
}

// noteBrowserMoved is called by View once it has placed the panel: if that
// moved it, a message tells the panel in the next Update (View can't
// return a command, and nothing else may arrive to carry it).
func (m *model) noteBrowserMoved() {
	if m.browser == nil || m.browserRect == m.browserLaid || m.browserMoveSent || m.send == nil {
		return
	}
	m.browserMoveSent = true
	send := m.send
	go send(browserMovedMsg{}) // Send waits for the loop View runs on
}

// closeBrowser takes the panel away; Chrome keeps running.
func (m *model) closeBrowser() tea.Cmd {
	if m.browser == nil {
		return nil
	}
	clear := m.browser.Clear()
	m.browser.Close()
	m.browser, m.browserFocused = nil, false
	return clear
}

// registerShower lets the agent's browser_show open the panel.
func (m *model) registerShower() {
	if m.opts.App == nil || m.opts.App.Browser == nil || m.send == nil {
		return
	}
	send := m.send
	m.panelOK.Store(m.panelAllowed())
	m.opts.App.Browser.SetShower(func() (string, bool) {
		if !m.panelOK.Load() {
			return "", false
		}
		send(openBrowserMsg{})
		return "Opened the browser in a panel beside the conversation. It follows the tab you work in; the user can click, scroll and type there (F3 moves the keyboard to it).", true
	})
}

// browserUpdate handles what belongs to the panel, before the rest of
// Update; handled says the message is used up.
func (m *model) browserUpdate(msg tea.Msg) (cmd tea.Cmd, handled bool) {
	switch msg := msg.(type) {
	case termbrowser.PanelMsg:
		if m.browser == nil {
			return nil, true
		}
		return m.browser.Update(msg), true
	case termbrowser.PanelClosedMsg:
		return m.closeBrowser(), true
	case uv.KittyGraphicsEvent:
		if m.browser != nil {
			return m.browser.Update(msg), true
		}
	case openBrowserMsg:
		if m.browser == nil && m.opts.App != nil && m.opts.App.Browser != nil && m.panelAllowed() {
			return m.openBrowser(), true
		}
		return nil, true
	case browserMovedMsg:
		m.browserMoveSent = false
		return m.syncBrowserLayout(), true
	case tea.WindowSizeMsg:
		m.panelOK.Store(m.panelAllowedAt(msg.Width))
	case tea.KeyPressMsg:
		if m.keys.action[msg.String()] == actToggleBrowser {
			return m.toggleBrowser(), true
		}
		// ctrl+c always reaches Larik, and an answer it needs (a
		// permission) takes the keyboard back.
		if m.browser != nil && m.browserFocused && m.perm == nil && msg.String() != "ctrl+c" {
			return m.toBrowser(msg), true
		}
	case tea.PasteMsg:
		if m.browser != nil && m.browserFocused && m.perm == nil {
			return m.toBrowser(msg), true
		}
	case tea.MouseClickMsg:
		if m.browser != nil && msg.Button == tea.MouseLeft && m.onDivider(msg.X, msg.Y) {
			m.browserResizing = true // dragging the divider resizes the panel
			return nil, true
		}
		if m.browser != nil {
			m.browserFocused = m.onBrowser(msg.X, msg.Y)
			if m.browserFocused {
				return m.toBrowser(msg), true
			}
		}
	case tea.MouseReleaseMsg:
		if m.browserResizing {
			m.browserResizing = false
			return nil, true
		}
		// A drag that began on the page ends there, wherever it lets go.
		if m.browser != nil && (m.browser.Dragging() || m.onBrowser(msg.X, msg.Y)) {
			return m.toBrowser(msg), true
		}
	case tea.MouseMotionMsg:
		if m.browserResizing {
			m.resizeBrowser(msg.X)
			return nil, true
		}
		if m.browser != nil {
			if m.browser.Dragging() || m.onBrowser(msg.X, msg.Y) {
				return m.toBrowser(msg), true
			}
			return nil, true // hover reporting is on only for the panel
		}
	case tea.MouseWheelMsg:
		if m.browser != nil && m.onBrowser(msg.X, msg.Y) {
			return m.toBrowser(msg), true
		}
	}
	return nil, false
}

// browserLook is what of the panel the rest of the screen shows: whether
// it is open, has the keyboard, and its header rows.
type browserLook struct {
	open, focused, resizing bool
	pct                     int
	header                  [2]string
}

func (m *model) browserLook() browserLook {
	if m.browser == nil {
		return browserLook{}
	}
	return browserLook{true, m.browserFocused, m.browserResizing, m.browserPct, m.browser.Header()}
}

// onDivider reports whether screen cell x, y is the column between the
// conversation and the panel.
func (m *model) onDivider(x, y int) bool {
	r := m.browserRect
	return r.cols > 0 && x == r.x-1 && y >= r.y && y < r.y+r.rows
}

// resizeBrowser puts the divider at column x, within browser.pane_size's
// bounds, for the rest of the session.
func (m *model) resizeBrowser(x int) {
	if m.width <= 0 {
		return
	}
	m.browserPct = min(max((m.width-x-1)*100/m.width, 20), 80)
}

// syncBrowserLayout tells the panel where the last frame put it, if that
// moved: the image is drawn by the panel, the cells around it by View.
func (m *model) syncBrowserLayout() tea.Cmd {
	if m.browser == nil || m.browserRect == m.browserLaid {
		return nil
	}
	r := m.browserRect
	m.browserLaid = r
	return m.browser.Layout(r.x, r.y, r.cols, r.rows)
}

// onBrowser reports whether screen cell x, y is on the panel as drawn.
func (m *model) onBrowser(x, y int) bool {
	r := m.browserRect
	return x >= r.x && x < r.x+r.cols && y >= r.y && y < r.y+r.rows
}

// toBrowser hands input to the panel, placed where it is on screen first
// so its coordinates are right.
func (m *model) toBrowser(msg tea.Msg) tea.Cmd {
	return tea.Batch(m.syncBrowserLayout(), m.browser.Update(msg))
}

// toggleBrowser is F3: open the panel, or move the keyboard to or from it.
func (m *model) toggleBrowser() tea.Cmd {
	if m.browser != nil {
		m.browserFocused = !m.browserFocused
		return nil
	}
	if m.opts.App == nil || m.opts.App.Browser == nil {
		return m.println(m.st.dim.Render("browser tools are off · /browser on to enable them"))
	}
	if !m.panelAllowed() {
		return m.showBrowserPane()
	}
	return m.openBrowser()
}

// panelAllowedAt is panelAllowed for a window width, for the agent's
// browser_show, which asks from another goroutine.
func (m *model) panelAllowedAt(width int) bool {
	c := m.opts.Config
	return m.send != nil && termbrowser.GraphicsTerminal() && width >= browserMinWidth+browserMinConversation+1 &&
		(c == nil || c.Browser.Show != "pane")
}
