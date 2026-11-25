package browserpane

import (
	"github.com/egoist/mygo"
	"larik/internal/desktop/panes"
)

// State owns the browser content of one Desktop pane. Window placement and
// remote forwarding remain with the Desktop host.
type State struct {
	Web                   *mygo.WebView
	URL, Address          string
	FocusAddress, Loading bool
	PageError             string
	Back, Forward         bool
	Observers             []func()
	Asking                *SiteAsk
	Download              *PageDownload
	Found                 mygo.FindResult
}

type SiteAsk struct {
	Origin      string
	Permissions []mygo.Permission
}

type PageDownload struct {
	Name, Path string
	Done       bool
	Err        string
}

func (*State) Kind() panes.Kind { return panes.Browser }

func (b *State) Close() {
	for _, off := range b.Observers {
		off()
	}
	b.Observers = nil
	if b.Web != nil {
		b.Web.Destroy()
	}
}
