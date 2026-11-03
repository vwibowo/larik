package desktop

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

// What each session runs on and has used, as its larik writes it into
// its status file: the model, what it has cost, how full its context is,
// and its permission mode. The toolbar says it for the focused tab, a
// session's tooltip for each, and list for scripts; a session whose
// context is nearly full says so in the sidebar, as /compact would free
// room before larik compacts on its own.

// contextWarn is how full a session's context is, in percent, before
// its row says so; at contextFull it's red.
const (
	contextWarn = 80
	contextFull = 95
)

// usageText is a session's usage in a line: "gpt-6-luna · 42% context ·
// $0.31 · auto". A model without a price shows no cost, and the default
// mode no mode.
func usageText(s session.Indexed) string {
	u := s.Usage
	var parts []string
	if model := cmp.Or(u.Model, s.Model); model != "" {
		parts = append(parts, model)
	}
	if pct := u.ContextPercent(); pct >= 0 {
		parts = append(parts, fmt.Sprintf("%d%% context", pct))
	}
	if u.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", u.CostUSD))
	}
	if u.Mode != "" && u.Mode != "default" {
		parts = append(parts, u.Mode)
	}
	return strings.Join(parts, " · ")
}

// contextMarker shows, beside a session's row, how full its context is,
// once that's past contextWarn, and reports whether it showed it.
func contextMarker(c *ui.Context, t *ui.Theme, u status.Usage) bool {
	pct := u.ContextPercent()
	if pct < contextWarn {
		return false
	}
	color := t.Warning
	if pct >= contextFull {
		color = t.Danger
	}
	tip := fmt.Sprintf("Context %d%% full (%dk of %dk tokens): /compact in the session frees room", pct, u.Tokens/1000, u.Window/1000)
	ui.Text(c, fmt.Sprintf("%d%%", pct)).FontSize(11).Bold().TextColor(color).SingleLine().Tooltip(tip).Label(tip)
	return true
}
