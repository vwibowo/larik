package tui

import (
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/termbrowser"
)

// panelModel is a model with an open browser panel on a session that has
// not started Chrome; layout and focus need nothing more.
func panelModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("LARIK_FORCE_KITTY", "1")
	m := testModel(t)
	s := termbrowser.New(termbrowser.Options{})
	t.Cleanup(s.Close)
	dir, err := os.MkdirTemp("", "lk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock, err := s.Serve(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := termbrowser.NewPanel(sock, func(tea.Msg) {})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	m.browser, m.browserFocused = p, true
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.View()                    // where the panel goes
	m.Update(browserMovedMsg{}) // the panel learns it
	return m
}

func TestBrowserPanelLayout(t *testing.T) {
	m := panelModel(t)
	m.showInfo = true
	if m.sessionSidebarWidth() != 0 {
		t.Error("the panel takes the sidebar's column")
	}
	if w := m.currentConversationWidth(); w != 120-60-1 {
		t.Errorf("conversation width %d beside a 60-column panel", w)
	}
	view := plain(m.View().Content)
	if !strings.Contains(view, "←") || !strings.Contains(view, "waiting for a page") {
		t.Errorf("the panel's toolbar should be on screen:\n%s", view)
	}
	if r := m.browserRect; r.x != 60 || r.y != 0 || r.cols != 60 || r.rows < 10 {
		t.Errorf("panel rectangle %+v", r)
	}
	if m.View().MouseMode != tea.MouseModeAllMotion {
		t.Error("pages in the panel need hover reporting")
	}
	m.width = 60 // too narrow to sit beside the conversation
	if m.browserPanelWidth() != 0 || m.currentConversationWidth() != 60 {
		t.Error("a narrow window gives the conversation the whole width")
	}
}

func TestBrowserPanelFocus(t *testing.T) {
	m := panelModel(t)
	typeText(m, "x")
	if m.input.Value() != "" {
		t.Fatal("keys go to the browser while it has the focus")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if m.browserFocused {
		t.Fatal("F3 gives the keyboard back to Larik")
	}
	typeText(m, "x")
	if m.input.Value() != "x" {
		t.Fatalf("keys reach the composer again: %q", m.input.Value())
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if !m.browserFocused {
		t.Fatal("F3 gives the keyboard to the browser again")
	}
	// A click on the conversation takes the keyboard back; one on the panel
	// gives it to the browser.
	m.Update(tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft})
	if m.browserFocused {
		t.Error("clicking the conversation focuses Larik")
	}
	m.Update(tea.MouseClickMsg{X: 90, Y: 10, Button: tea.MouseLeft})
	if !m.browserFocused {
		t.Error("clicking the panel focuses the browser")
	}
	m.Update(termbrowser.PanelClosedMsg{})
	if m.browser != nil || m.browserFocused {
		t.Error("ctrl+q in the browser closes the panel")
	}
}

// A drag from the page that ends over the conversation still releases in
// the page.
func TestBrowserPanelDragEndsInPage(t *testing.T) {
	m := panelModel(t)
	m.Update(tea.MouseClickMsg{X: 90, Y: 10, Button: tea.MouseLeft})
	if !m.browser.Dragging() {
		t.Fatal("a press on the page is held")
	}
	m.Update(tea.MouseReleaseMsg{X: 5, Y: 5, Button: tea.MouseLeft})
	if m.browser.Dragging() {
		t.Fatal("the release over the conversation reaches the page")
	}
}

// What only the panel draws (frames, hover) doesn't rebuild the screen.
func TestBrowserPanelEventsReuseTheView(t *testing.T) {
	m := panelModel(t)
	first := m.View().Content
	m.Update(termbrowser.PanelMsg{})
	if !m.reuseView {
		t.Fatal("a panel message that changes nothing keeps the view")
	}
	if m.View().Content != first || m.reuseView {
		t.Fatal("the kept view is returned once")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyF3})
	if m.reuseView {
		t.Fatal("moving the keyboard redraws")
	}
	if m.View().Content == first {
		t.Error("the header and composer show where the keyboard went")
	}
}
