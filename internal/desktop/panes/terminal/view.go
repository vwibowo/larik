package terminalpane

import (
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

func View(c *ui.Context, id string, term *terminal.Terminal, findBar func()) ui.Element {
	if findBar != nil {
		findBar()
	}
	if term != nil {
		return terminal.View(c.Key(id), term).Grow(1)
	}
	return ui.Box(c.Key(id)).Grow(1)
}
