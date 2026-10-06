package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/permission"
)

type styles struct {
	accent, dim, user, err, warn, ok, diffAdd, diffDel, thinking, box, modal, statusMode lipgloss.Style
	// Faint backgrounds behind highlighted added and removed code.
	diffAddBg, diffDelBg lipgloss.Style
	// Footer chips: colored text only, so the terminal background shows through.
	chip, chipWarn, chipPlain lipgloss.Style
	// One color per permission mode (default stays plain) and a magenta ramp
	// for effort, brighter as it rises, so both read at a glance.
	modePlan, modeAccept, modeAuto, modeYolo lipgloss.Style
	effort                                   [5]lipgloss.Style
}

// modeStyle is the footer chip style for a permission mode.
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
	c := lipgloss.LightDark(isDark)
	pick := func(light, dark string) color.Color { return c(lipgloss.Color(light), lipgloss.Color(dark)) }
	paletteColor := func(index int, fallback color.Color) color.Color {
		if p != nil && p.ANSI[index] != "" {
			return lipgloss.Color(p.ANSI[index])
		}
		return fallback
	}
	foreground := pick("#1F1F24", "#ECECF0")
	if p != nil && p.Foreground != "" {
		foreground = lipgloss.Color(p.Foreground)
	}
	accent := paletteColor(6, pick("#0D9488", "#2DD4BF"))
	if p != nil && p.ANSI[14] != "" {
		accent = lipgloss.Color(p.ANSI[14])
	}
	dim := paletteColor(8, pick("#6B6B76", "#7D7E87"))
	err := paletteColor(1, pick("#C0352B", "#FF7A70"))
	warn := paletteColor(3, pick("#9A6700", "#E3B341"))
	ok := paletteColor(2, pick("#1A7F37", "#5CCB7A"))
	chipFg := func(fg color.Color) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(fg).Bold(true).Padding(0, 1)
	}
	ramp := func(light, dark string, bold bool) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(pick(light, dark)).Bold(bold)
	}
	return styles{
		accent:     lipgloss.NewStyle().Foreground(accent).Bold(true),
		dim:        lipgloss.NewStyle().Foreground(dim),
		user:       lipgloss.NewStyle().Foreground(foreground).Bold(true),
		err:        lipgloss.NewStyle().Foreground(err),
		warn:       lipgloss.NewStyle().Foreground(warn),
		ok:         lipgloss.NewStyle().Foreground(ok),
		diffAdd:    lipgloss.NewStyle().Foreground(ok),
		diffDel:    lipgloss.NewStyle().Foreground(err),
		diffAddBg:  lipgloss.NewStyle().Background(pick("#E6F4EA", "#15261B")),
		diffDelBg:  lipgloss.NewStyle().Background(pick("#FBE9E7", "#2D1717")),
		thinking:   lipgloss.NewStyle().Foreground(dim).Italic(true),
		box:        lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(dim).Padding(0, 1),
		modal:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1),
		statusMode: lipgloss.NewStyle().Foreground(accent),
		chip:       lipgloss.NewStyle().Foreground(accent).Bold(true).Padding(0, 1),
		chipWarn:   lipgloss.NewStyle().Foreground(warn).Bold(true).Padding(0, 1),
		chipPlain:  lipgloss.NewStyle().Foreground(foreground).Padding(0, 1),
		modePlan:   chipFg(pick("#2563EB", "#60A5FA")),
		modeAccept: chipFg(pick("#7C3AED", "#A78BFA")),
		modeAuto:   chipFg(pick("#15803D", "#4ADE80")),
		modeYolo:   chipFg(pick("#C0352B", "#FF7A70")),
		effort: [5]lipgloss.Style{
			ramp("#8A4B82", "#9D6B99", false), ramp("#A8399A", "#C56FBE", false), ramp("#C026A3", "#E879D9", false),
			ramp("#A21CAF", "#F08FE3", true), ramp("#86198F", "#FFA8F0", true),
		},
	}
}
