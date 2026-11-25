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
	if tb.isBrowser() {
		if tb.browserState().PageError != "" {
			return "Page load failed: " + tb.browserState().PageError, false, true
		}
		if tb.browserState().Loading {
			return "", true, false
		}
	}
	if f := tb.filesState(); f != nil {
		if f.HasQuery() {
			if f.Search.Err != "" {
				return "File search failed: " + f.Search.Err, false, true
			}
			if f.Search.Loading {
				return "", true, false
			}
		}
		if strings.HasPrefix(f.Preview.Note, "Can't ") {
			return f.Preview.Note, false, true
		}
		if f.Preview.Loading {
			return "", true, false
		}
		if root := f.Dirs[f.Root]; root != nil {
			if root.Err != "" {
				return "Can't open folder: " + root.Err, false, true
			}
			if root.Loading {
				return "", true, false
			}
		}
	}
	if d := tb.databaseState(); d != nil {
		v := d.CurrentResult()
		if d.Loading {
			return "", true, false
		}
		for _, status := range []string{d.Status, d.FunctionStatus, v.Status} {
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
