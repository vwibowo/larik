package panes

import "github.com/egoist/mygo/ui"

// DatabaseView is the window service boundary used by the Database renderer.
// The renderer owns layout; the host supplies pane state and actions.
type DatabaseView struct {
	ID                                                              string
	Narrow, Compact                                                 *bool
	LegacyEditorPercent, EditorHeight, BrowserWidth, InspectorWidth *float32
	Region                                                          *int
	Editing, ShowBrowser, ShowDetails, HasSelectedRow               func() bool
	Profile                                                         func() string
	ProfileExists                                                   func(string) bool
	StoreError                                                      func() error
	Header, Form, Empty, Objects, Center, Inspector                 func()
}

type DatabaseRenderer func(*ui.Context, *ui.Theme, DatabaseView) ui.Element

const (
	DatabaseTablesRegion = iota
	DatabaseResultsRegion
	DatabaseDetailsRegion
)
