package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

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
	modePlan, modeAccept, modeYolo lipgloss.Style
	effort                         [5]lipgloss.Style
}

// modeStyle is the footer chip style for a permission mode.
func (s styles) modeStyle(mode permission.Mode) lipgloss.Style {
	switch mode {
	case permission.ModePlan:
		return s.modePlan
	case permission.ModeAcceptEdits:
		return s.modeAccept
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

func newStyles(isDark bool) styles {
	c := lipgloss.LightDark(isDark)
	pick := func(light, dark string) color.Color { return c(lipgloss.Color(light), lipgloss.Color(dark)) }
	accent := pick("#0D9488", "#2DD4BF")
	chipFg := func(fg color.Color) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(fg).Bold(true).Padding(0, 1)
	}
	ramp := func(light, dark string, bold bool) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(pick(light, dark)).Bold(bold)
	}
	return styles{
		accent:     lipgloss.NewStyle().Foreground(accent).Bold(true),
		dim:        lipgloss.NewStyle().Foreground(pick("#6B6B76", "#7D7E87")),
		user:       lipgloss.NewStyle().Foreground(pick("#1F1F24", "#ECECF0")).Bold(true),
		err:        lipgloss.NewStyle().Foreground(pick("#C0352B", "#FF7A70")),
		warn:       lipgloss.NewStyle().Foreground(pick("#9A6700", "#E3B341")),
		ok:         lipgloss.NewStyle().Foreground(pick("#1A7F37", "#5CCB7A")),
		diffAdd:    lipgloss.NewStyle().Foreground(pick("#1A7F37", "#5CCB7A")),
		diffDel:    lipgloss.NewStyle().Foreground(pick("#C0352B", "#FF7A70")),
		diffAddBg:  lipgloss.NewStyle().Background(pick("#E6F4EA", "#15261B")),
		diffDelBg:  lipgloss.NewStyle().Background(pick("#FBE9E7", "#2D1717")),
		thinking:   lipgloss.NewStyle().Foreground(pick("#6B6B76", "#7D7E87")).Italic(true),
		box:        lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(pick("#C9C9D1", "#3A3A42")).Padding(0, 1),
		modal:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1),
		statusMode: lipgloss.NewStyle().Foreground(accent),
		chip:       lipgloss.NewStyle().Foreground(accent).Bold(true).Padding(0, 1),
		chipWarn:   lipgloss.NewStyle().Foreground(pick("#9A6700", "#E3B341")).Bold(true).Padding(0, 1),
		chipPlain:  lipgloss.NewStyle().Foreground(pick("#1F1F24", "#ECECF0")).Padding(0, 1),
		modePlan:   chipFg(pick("#2563EB", "#60A5FA")),
		modeAccept: chipFg(pick("#7C3AED", "#A78BFA")),
		modeYolo:   chipFg(pick("#C0352B", "#FF7A70")),
		effort: [5]lipgloss.Style{
			ramp("#8A4B82", "#9D6B99", false), ramp("#A8399A", "#C56FBE", false), ramp("#C026A3", "#E879D9", false),
			ramp("#A21CAF", "#F08FE3", true), ramp("#86198F", "#FFA8F0", true),
		},
	}
}
