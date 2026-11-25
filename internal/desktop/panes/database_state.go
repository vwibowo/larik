package panes

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"larik/internal/database"
)

type DatabaseState struct {
	Ctx                                            context.Context
	Cancel                                         context.CancelFunc
	Seq                                            uint64
	Profile                                        string
	Driver                                         string
	EmptyChoice                                    string
	Tables                                         []database.Table
	SchemaOpen                                     map[string]*bool
	Schema, Table                                  string
	SQL                                            *editor.State
	FunctionEditor                                 *editor.State
	FunctionStatus                                 string
	ShowFunctionDefinition                         bool
	ShowStructure                                  bool
	ExportSeq                                      uint64
	Search                                         string
	SchemaFilter                                   string
	PageSize                                       int
	Results                                        [3]DatabaseResultView
	ActiveResult, Region                           int
	Loading                                        bool
	Status                                         string
	Editing, Deleting                              bool
	OldName, DraftName, DraftDriver, DraftValue    string
	DraftHost, DraftPort, DraftDatabase, DraftUser string
	DraftPasswordEnv, DraftPassword, DraftSSLMode  string
	DraftClearPassword                             bool
	DraftTrust                                     bool
	DraftAgent                                     bool
	TestingConnection                              bool
	BrowserWidth, InspectorWidth, EditorHeight     float32
	LegacyEditorPercent                            float32
	ShowBrowser, ShowDetails                       bool
	Narrow, Compact                                bool
}

type DatabaseResultView struct {
	Result      database.Result
	SelectedRow int
	Offset      int
	Elapsed     time.Duration
	Status      string
	Source      string
	Grid        ui.ListState
	Selection   ui.Selection[int]
	FocusGrid   bool
}

func NewDatabaseState() *DatabaseState {
	ctx, cancel := context.WithCancel(context.Background())
	d := &DatabaseState{
		Ctx: ctx, Cancel: cancel, DraftDriver: "sqlite",
		BrowserWidth: 26, InspectorWidth: 76, EditorHeight: 180,
		PageSize: database.DefaultRows, ShowBrowser: true, Region: DatabaseResultsRegion,
	}
	for i := range d.Results {
		d.Results[i].SelectedRow = -1
	}
	return d
}

func (d *DatabaseState) Kind() Kind { return Database }

func (d *DatabaseState) CurrentResult() *DatabaseResultView { return &d.Results[d.ActiveResult] }
func (d *DatabaseState) SelectResult(index int) {
	d.ActiveResult = index
	d.Region = DatabaseResultsRegion
}
func (d *DatabaseState) HasSelectedRow() bool {
	v := d.CurrentResult()
	return v.SelectedRow >= 0 && v.SelectedRow < len(v.Result.Rows)
}

func (d *DatabaseState) Close() {
	if d.Cancel != nil {
		d.Cancel()
	}
}

func (d *DatabaseState) ClearProfile() {
	d.Seq++
	d.Profile, d.Schema, d.Table, d.Status = "", "", "", ""
	d.Tables = nil
	for i := range d.Results {
		d.Results[i] = DatabaseResultView{SelectedRow: -1}
	}
}
