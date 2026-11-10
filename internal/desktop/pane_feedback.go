package desktop

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// paneFeedbackState reports whether a pane has in-flight work and any useful
// failure or status message. Loading is shown beside the pane title; messages
// remain in the shared feedback strip.
func paneFeedbackState(tb *tab) (message string, loading, failed bool) {
	if tb == nil {
		return "", false, false
	}
	if tb.browser {
		if tb.pageError != "" {
			return "Page load failed: " + tb.pageError, false, true
		}
		if tb.loading {
			return "", true, false
		}
	}
	if f := tb.files; f != nil {
		if f.hasQuery() {
			if f.search.err != "" {
				return "File search failed: " + f.search.err, false, true
			}
			if f.search.loading {
				return "", true, false
			}
		}
		if strings.HasPrefix(f.preview.note, "Can't ") {
			return f.preview.note, false, true
		}
		if f.preview.loading {
			return "", true, false
		}
		if root := f.dirs[f.root]; root != nil {
			if root.err != "" {
				return "Can't open folder: " + root.err, false, true
			}
			if root.loading {
				return "", true, false
			}
		}
	}
	if d := tb.database; d != nil {
		v := d.currentResult()
		if d.loading {
			return "", true, false
		}
		for _, status := range []string{d.status, d.functionStatus, v.status} {
			if strings.HasPrefix(status, "Loading ") {
				return "", true, false
			}
			if status != "" && !strings.HasPrefix(status, "Copied ") && !strings.HasPrefix(status, "Exported ") && status != "Connection successful" {
				return status, false, true
			}
		}
	}
	return "", false, false
}

func (a *App) paneFeedback(c *ui.Context, theme *ui.Theme, tb *tab) {
	message, _, failed := paneFeedbackState(tb)
	if message == "" {
		return
	}
	color := theme.TextMuted
	background := theme.Surface
	if failed {
		color, background = theme.Danger, theme.Danger.Alpha(0.08)
	}
	ui.Row(c.Key(tb.id+"-feedback")).Gap(8).Padding(7, 12).AlignItems(ui.Center).
		Background(background).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
		if failed {
			ui.Icon(c, iconError).FontSize(13).TextColor(color)
		}
		ui.Text(c, message).FontSize(12).TextColor(color).Grow(1).Shrink(1)
	})
}
