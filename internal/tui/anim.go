package tui

import (
	"math"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/harmonica"
)

// Motion in the TUI: a row of blocks with the brand gradient running
// through it, a shimmer across the activity label and a spring on the
// streamed-token count. All of it advances on the spinner's tick, which
// only runs while something is in progress, so an idle larik draws nothing.

const motionFPS = 12

// spinnerCells is how many blocks the row spinner has.
const spinnerCells = 4

type motion struct {
	level  string // the animations setting: on, reduced or off
	frame  int
	spring harmonica.Spring
	// tokens eases toward the turn's token estimate.
	tokens, tokensVel float64
}

func newMotion(level string) motion {
	return motion{level: level, spring: harmonica.NewSpring(harmonica.FPS(motionFPS), 6.0, 1.0)}
}

// setAnimations applies the animations setting. With motion off the tick
// slows to once a second, which still keeps the elapsed time current.
func (m *model) setAnimations(level string) {
	m.motion.level = level
	every := time.Second / motionFPS
	if level == "off" {
		every = time.Second
	}
	m.spin.Spinner = spinner.Spinner{Frames: []string{""}, FPS: every}
}

// step advances one frame toward the turn's token estimate.
func (mo *motion) step(tokens int) {
	mo.frame++
	if mo.level != "on" {
		mo.tokens, mo.tokensVel = float64(tokens), 0
		return
	}
	mo.tokens, mo.tokensVel = mo.spring.Update(mo.tokens, mo.tokensVel, float64(tokens))
}

// resetTokens starts the count over for a new turn.
func (mo *motion) resetTokens() { mo.tokens, mo.tokensVel = 0, 0 }

// shownTokens is the token count as drawn: the eased value, never above
// the estimate it is easing toward.
func (m *model) shownTokens() int {
	target := m.turnChars / 4
	if m.motion.level != "on" {
		return target
	}
	return min(max(int(math.Round(m.motion.tokens)), 0), target)
}

// rowSpinner is larik's spinner: a row of blocks the gradient runs along.
func (m *model) rowSpinner() string {
	ramp := m.st.spinRamp
	if len(ramp) == 0 {
		return m.st.accent.Render(strings.Repeat("▪", spinnerCells))
	}
	frame := m.motion.frame
	if m.motion.level == "off" {
		frame = 0
	}
	var b strings.Builder
	for i := range spinnerCells {
		b.WriteString(ramp[((frame-i)%len(ramp)+len(ramp))%len(ramp)].Render("▪"))
	}
	return b.String()
}

// pulse is the dot before a running call: it breathes between the accent
// and dim colors, twice a second.
func (m *model) pulse() string {
	if m.motion.level == "off" || m.motion.frame%motionFPS < motionFPS/2 {
		return m.st.accent.Render("●")
	}
	return m.st.dim.Render("●")
}

// shimmer draws label with a bright band sweeping across it, spark at its
// center and accent at its edges.
func (m *model) shimmer(label string) string {
	if m.motion.level != "on" {
		return label
	}
	runes := []rune(label)
	const lead = 4 // frames before the band reaches the first letter
	p := m.motion.frame%(len(runes)+2*lead+4) - lead
	var b strings.Builder
	plain := lipgloss.NewStyle()
	for i, r := range runes {
		style := plain
		switch d := abs(i - p); {
		case d == 0:
			style = m.st.spark.UnsetBold()
		case d <= 2:
			style = m.st.accent.UnsetBold()
		}
		b.WriteString(style.Render(string(r)))
	}
	return b.String()
}
