package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

type styles struct {
	accent, dim, user, err, warn, ok, diffAdd, diffDel, thinking, box, modal, statusMode lipgloss.Style
	// Footer chips.
	chip, chipWarn, chipPlain lipgloss.Style
}

func newStyles(isDark bool) styles {
	c := lipgloss.LightDark(isDark)
	pick := func(light, dark string) color.Color { return c(lipgloss.Color(light), lipgloss.Color(dark)) }
	accent := pick("#5B4BD5", "#A99BFF")
	return styles{
		accent:     lipgloss.NewStyle().Foreground(accent).Bold(true),
		dim:        lipgloss.NewStyle().Foreground(pick("#6B6B76", "#8A8A96")),
		user:       lipgloss.NewStyle().Foreground(pick("#1F1F24", "#E8E8EE")).Bold(true),
		err:        lipgloss.NewStyle().Foreground(pick("#C0352B", "#FF7A70")),
		warn:       lipgloss.NewStyle().Foreground(pick("#9A6700", "#E3B341")),
		ok:         lipgloss.NewStyle().Foreground(pick("#1A7F37", "#5CCB7A")),
		diffAdd:    lipgloss.NewStyle().Foreground(pick("#1A7F37", "#5CCB7A")),
		diffDel:    lipgloss.NewStyle().Foreground(pick("#C0352B", "#FF7A70")),
		thinking:   lipgloss.NewStyle().Foreground(pick("#6B6B76", "#8A8A96")).Italic(true),
		box:        lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(pick("#C9C9D1", "#44444F")).Padding(0, 1),
		modal:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(0, 1),
		statusMode: lipgloss.NewStyle().Foreground(accent),
		chip:       lipgloss.NewStyle().Foreground(accent).Background(pick("#ECE9FB", "#2A2640")).Bold(true).Padding(0, 1),
		chipWarn:   lipgloss.NewStyle().Foreground(pick("#9A6700", "#E3B341")).Background(pick("#FBF1DC", "#3A3120")).Bold(true).Padding(0, 1),
		chipPlain:  lipgloss.NewStyle().Foreground(pick("#1F1F24", "#E8E8EE")).Background(pick("#EBEBF0", "#24242D")).Padding(0, 1),
	}
}
