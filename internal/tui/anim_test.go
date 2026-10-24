package tui

import (
	"strings"
	"testing"
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
