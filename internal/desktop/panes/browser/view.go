package browserpane

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// View keeps browser chrome and the native web view in one render order.
func View(c *ui.Context, id string, web *mygo.WebView, tabs, address, site, findBar func()) ui.Element {
	tabs()
	address()
	site()
	if findBar != nil {
		findBar()
	}
	if web != nil {
		return ui.WebView(c.Key(id), web).Grow(1)
	}
	return ui.Box(c.Key(id)).Grow(1)
}
