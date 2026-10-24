package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/tone"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/permission"
)

// The palette is CharmTone's, contrast-tuned per background. The terminal's
// own background always shows through: only small elements (your prompts,
// the mode pill, the selected picker row and diff tints) paint one.
type styles struct {
	accent, dim, user, err, warn, ok, diffAdd, diffDel, thinking, box, modal, statusMode lipgloss.Style
	// Faint backgrounds behind highlighted added and removed code.
	diffAddBg, diffDelBg lipgloss.Style
	// Footer chips: colored text only, so the terminal background shows through.
	chip, chipWarn, chipPlain lipgloss.Style
	// One color per permission mode (default stays plain) and a ramp from
	// accent to spark for effort, brighter as it rises, so both read at a glance.
	modePlan, modeAccept, modeAuto, modeYolo lipgloss.Style
	effort                                   [5]lipgloss.Style
	// spark is the far end of the brand gradient, accent the near end.
	spark lipgloss.Style
	// band is the faint background behind your prompts and the selected
	// picker row; pillInk is the text on a filled mode pill.
	band            lipgloss.Style
	pillInk, pillBg color.Color
	// accentColor and sparkColor are the gradient stops for the brand mark,
	// the spinner and the shimmer.
	accentColor, sparkColor, dimColor, fgColor color.Color
	// spinRamp is the gradient out and back that runs along the spinner.
	spinRamp []lipgloss.Style
}

// modeStyle is the colored-text style for a permission mode.
func (s styles) modeStyle(mode permission.Mode) lipgloss.Style {
	switch mode {
	case permission.ModePlan:
		return s.modePlan
	case permission.ModeAcceptEdits:
		return s.modeAccept
	case permission.ModeAuto:
		return s.modeAuto
	case permission.ModeYolo:
		return s.modeYolo
	}
	return s.chipPlain
}

// modePill is the footer's filled chip for a permission mode: the mode's
// color behind dark text, or a neutral fill for the default mode.
func (s styles) modePill(mode permission.Mode) lipgloss.Style {
	pill := lipgloss.NewStyle().Bold(true).Padding(0, 1)
	if mode == permission.ModeDefault || mode == "" {
		return pill.Foreground(s.fgColor).Background(s.pillBg)
	}
	return pill.Foreground(s.pillInk).Background(s.modeStyle(mode).GetForeground())
}

// gradient is n colors from accent to spark.
func (s styles) gradient(n int) []color.Color {
	if n <= 0 {
		return nil
	}
	if n == 1 {
		return []color.Color{s.accentColor}
	}
	return lipgloss.Blend1D(n, s.accentColor, s.sparkColor)
}

// effortMarks are the bar glyphs for low through max.
var effortMarks = [5]string{"▂", "▃", "▅", "▆", "█"}

// effortStyle is the ramp style and bar glyph for a non-default effort.
func (s styles) effortStyle(e llm.Effort) (lipgloss.Style, string) {
	for i, l := range [5]llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax} {
		if e == l {
			return s.effort[i], effortMarks[i]
		}
	}
	return lipgloss.NewStyle(), ""
}

func newStyles(isDark bool) styles { return newStylesWithPalette(isDark, nil) }

func newStylesWithPalette(isDark bool, p *config.ThemePalette) styles {
	pick := lipgloss.LightDark(isDark)
	paletteColor := func(index int, fallback color.Color) color.Color {
		if p != nil && p.ANSI[index] != "" {
			return lipgloss.Color(p.ANSI[index])
		}
		return fallback
	}
	foreground := pick(tone.Pepper, tone.Sash)
	if p != nil && p.Foreground != "" {
		foreground = lipgloss.Color(p.Foreground)
	}
	accent := paletteColor(6, pick(lipgloss.Color("#0D9488"), tone.Turtle))
	if p != nil && p.ANSI[14] != "" {
		accent = lipgloss.Color(p.ANSI[14])
	}
	spark := paletteColor(13, pick(tone.Darple, tone.Hazy))
	dim := paletteColor(8, pick(tone.Oyster, tone.Squid))
	err := paletteColor(1, pick(tone.Pom, tone.Coral))
	warn := paletteColor(3, pick(lipgloss.Color("#9A6700"), tone.Tang))
	ok := paletteColor(2, pick(tone.Pickle, tone.Guac))

	// The band and the neutral pill sit a step off the terminal background
	// toward the text color, so they read on any scheme.
	band, pillBg := pick(lipgloss.Color("#E3F6F5"), lipgloss.Color("#16302F")), pick(tone.Steep, tone.Char)
	pillInk := pick(tone.Salt, tone.Pepper)
	if p != nil && p.Background != "" {
		bg := lipgloss.Color(p.Background)
		steps := lipgloss.Blend1D(12, bg, foreground)
		band, pillBg, pillInk = steps[1], steps[2], bg
	}

	chipFg := func(fg color.Color) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(fg).Bold(true).Padding(0, 1)
	}
	st := styles{
		accent:      lipgloss.NewStyle().Foreground(accent).Bold(true),
		spark:       lipgloss.NewStyle().Foreground(spark).Bold(true),
		dim:         lipgloss.NewStyle().Foreground(dim),
		user:        lipgloss.NewStyle().Foreground(foreground).Bold(true),
		err:         lipgloss.NewStyle().Foreground(err),
		warn:        lipgloss.NewStyle().Foreground(warn),
		ok:          lipgloss.NewStyle().Foreground(ok),
		diffAdd:     lipgloss.NewStyle().Foreground(ok),
		diffDel:     lipgloss.NewStyle().Foreground(err),
		diffAddBg:   lipgloss.NewStyle().Background(pick(lipgloss.Color("#E6F4EA"), tone.Spinach)),
		diffDelBg:   lipgloss.NewStyle().Background(pick(lipgloss.Color("#FBE9E7"), tone.Toast)),
		thinking:    lipgloss.NewStyle().Foreground(dim).Italic(true),
		box:         lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(dim).Padding(0, 1),
		modal:       lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForegroundBlend(accent, spark).Padding(0, 1),
		statusMode:  lipgloss.NewStyle().Foreground(accent),
		chip:        lipgloss.NewStyle().Foreground(accent).Bold(true).Padding(0, 1),
		chipWarn:    lipgloss.NewStyle().Foreground(warn).Bold(true).Padding(0, 1),
		chipPlain:   lipgloss.NewStyle().Foreground(foreground).Padding(0, 1),
		modePlan:    chipFg(pick(tone.Damson, tone.Sardine)),
		modeAccept:  chipFg(pick(tone.Prince, tone.Hazy)),
		modeAuto:    chipFg(pick(tone.Pickle, tone.Julep)),
		modeYolo:    chipFg(pick(tone.Sriracha, tone.Coral)),
		band:        lipgloss.NewStyle().Background(band),
		pillInk:     pillInk,
		pillBg:      pillBg,
		accentColor: accent,
		sparkColor:  spark,
		dimColor:    dim,
		fgColor:     foreground,
	}
	for i, c := range st.gradient(len(st.effort)) {
		st.effort[i] = lipgloss.NewStyle().Foreground(c).Bold(i >= 3)
	}
	ramp := st.gradient(spinnerCells)
	for i := len(ramp) - 2; i > 0; i-- {
		ramp = append(ramp, ramp[i])
	}
	for _, c := range ramp {
		st.spinRamp = append(st.spinRamp, lipgloss.NewStyle().Foreground(c))
	}
	return st
}

// brandMark is larik's mark: a row of three blocks from accent to spark.
func (m *model) brandMark() string {
	var b strings.Builder
	for _, c := range m.st.gradient(3) {
		b.WriteString(lipgloss.NewStyle().Foreground(c).Render("▰"))
	}
	return b.String()
}

// toolMark is a finished call's mark: a check, or a cross when it failed.
func (m *model) toolMark(failed bool) string {
	if failed {
		return m.st.err.Render("✗")
	}
	return m.st.ok.Render("✓")
}

// toolHead is a call's title with the tool's name in bold and its
// argument in parentheses after it.
func (m *model) toolHead(name string, input []byte) string {
	title := toolTitle(name, input, m.shortPaths)
	i := strings.Index(title, "(")
	if i < 0 || !strings.HasSuffix(title, ")") {
		return lipgloss.NewStyle().Bold(true).Render(title)
	}
	return lipgloss.NewStyle().Bold(true).Render(title[:i]) + m.st.dim.Render("(") + title[i+1:len(title)-1] + m.st.dim.Render(")")
}

// assistantBullet marks the start of a reply: the accent dot takes the
// first line's left margin when it has one.
func (m *model) assistantBullet(rendered string) string {
	if strings.HasPrefix(rendered, "  ") {
		return m.st.accent.Render("●") + " " + rendered[2:]
	}
	return rendered
}
