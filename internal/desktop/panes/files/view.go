package filespane

import "github.com/egoist/mygo/ui"

func View(c *ui.Context, id string, narrow, previewOnly *bool, treeWidth *float32, tree, preview func()) ui.Element {
	pane := ui.Column(c.Key(id + "-files")).Grow(1).MinWidth(0).Focusable()
	if width := pane.Bounds().W; width > 0 {
		*narrow = width < 520
		if !*narrow {
			*previewOnly = false
		}
	}
	return pane.Children(func() {
		if *narrow {
			if *previewOnly {
				preview()
			} else {
				tree()
			}
			return
		}
		ui.Split(c, treeWidth, tree, preview).Grow(1)
	})
}
