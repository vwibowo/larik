package filespane

import (
	"context"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"larik/internal/desktop/panes"
)

// State owns the navigation, search, and preview of one Files pane.
type State struct {
	Root, Query, Selected  string
	PinnedRoot             bool
	Picked, Rows, LastRows []string
	Anchor                 string
	ShowHidden             bool
	ShowIgnored            bool
	Wrap                   bool
	FocusSearch            bool
	TreeW                  float32
	Narrow, PreviewOnly    bool
	Dirs                   map[string]*Directory
	Open                   map[string]*bool
	Search                 Search
	Preview                Preview
	Ctx                    context.Context
	Cancel                 context.CancelFunc
	SearchCancel           context.CancelFunc
	SearchSeq              uint64
	PreviewSeq             uint64
}

type Entry struct {
	Path string
	Dir  bool
}

type Directory struct {
	Entries []Entry
	Err     string
	Loaded  bool
	Loading bool
}

type Search struct {
	Tree    map[string][]Entry
	Matches int
	More    bool
	Loading bool
	Err     string
}

type Preview struct {
	Path, Text, Note string
	Image            *ui.Bitmap
	Code             *editor.State
	Pdf              bool
	Web              *mygo.WebView
	WebOff           []func()
	Size             int64
	Loading          bool
}

func NewState(root string) *State {
	ctx, cancel := context.WithCancel(context.Background())
	return &State{Root: root, TreeW: 220, Dirs: map[string]*Directory{}, Open: map[string]*bool{}, Ctx: ctx, Cancel: cancel}
}

func (f *State) Kind() panes.Kind { return panes.Files }
func (f *State) HasQuery() bool   { return strings.TrimSpace(f.Query) != "" }

func (f *State) Close() {
	f.ClosePreview()
	if f.SearchCancel != nil {
		f.SearchCancel()
	}
	if f.Cancel != nil {
		f.Cancel()
	}
}

func (f *State) ClosePreview() {
	for _, off := range f.Preview.WebOff {
		off()
	}
	if f.Preview.Web != nil {
		f.Preview.Web.Destroy()
	}
	f.Preview.WebOff = nil
	f.Preview.Web = nil
}
