package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// The spinner moves with motion on and holds still with it off; the
// shimmer never changes the label's text.
func TestRowSpinnerAndShimmer(t *testing.T) {
	m := testModel(t)
	first := m.rowSpinner()
	m.motion.step(0)
	if m.rowSpinner() == first {
		t.Error("the spinner should move from one frame to the next")
	}
	if got := plain(m.rowSpinner()); got != strings.Repeat("▪", spinnerCells) {
		t.Errorf("spinner = %q", got)
	}
	for range 5 {
		m.motion.step(0)
		if got := plain(m.shimmer("Thinking…")); got != "Thinking…" {
			t.Fatalf("shimmer changed the text: %q", got)
		}
	}

	m.setAnimations("off")
	still := m.rowSpinner()
	m.motion.step(0)
	if m.rowSpinner() != still {
		t.Error("with animations off the spinner should hold still")
	}
	if m.shimmer("Thinking…") != "Thinking…" {
		t.Error("with animations off the label should be plain")
	}
}

// The token count eases up to the estimate without passing it, and jumps
// straight there when motion is reduced.
func TestTokenCountEases(t *testing.T) {
	m := testModel(t)
	m.turnChars = 4000
	m.motion.step(1000)
	if n := m.shownTokens(); n <= 0 || n >= 1000 {
		t.Fatalf("after one frame the count should be on its way: %d", n)
	}
	for range 5 * motionFPS {
		m.motion.step(1000)
		if n := m.shownTokens(); n > 1000 {
			t.Fatalf("the count passed the estimate: %d", n)
		}
	}
	if n := m.shownTokens(); n != 1000 {
		t.Fatalf("the count should settle on the estimate: %d", n)
	}
	m.setAnimations("reduced")
	m.motion.resetTokens()
	if n := m.shownTokens(); n != 1000 {
		t.Fatalf("reduced motion should show the estimate at once: %d", n)
	}
}

// A copy confirmation shows in the corner and goes away on its own,
// leaving the conversation as it was.
func TestToastOverlaysAndExpires(t *testing.T) {
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	before := len(m.convLines)
	_, cmd := m.Update(toastMsg("copied the last reply (3 lines)"))
	if cmd == nil || len(m.convLines) != before {
		t.Fatal("a toast should schedule its expiry and leave the conversation alone")
	}
	view := plain(m.View().Content)
	top := strings.SplitN(view, "\n", 3)
	if len(top) < 2 || !strings.Contains(top[1], "✓ copied the last reply") {
		t.Fatalf("the toast should sit at the top right:\n%s", view)
	}
	m.Update(toastExpiredMsg{id: m.toastID - 1})
	if m.toast == "" {
		t.Fatal("an older toast's expiry must not close a newer one")
	}
	m.Update(toastExpiredMsg{id: m.toastID})
	if strings.Contains(plain(m.View().Content), "copied") {
		t.Fatal("the toast should be gone once it expires")
	}
}
