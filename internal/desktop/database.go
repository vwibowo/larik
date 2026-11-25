package desktop

import (
	"context"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"larik/internal/database"
	"larik/internal/desktop/panes"
)

type databaseState = panes.DatabaseState

func (t *tab) databaseState() *databaseState {
	if t == nil {
		return nil
	}
	d, _ := t.pane.(*databaseState)
	return d
}

type databaseResultView = panes.DatabaseResultView

const (
	databaseData = iota
	databaseQuery
	databaseAgent
)

const (
	databaseTablesRegion  = panes.DatabaseTablesRegion
	databaseResultsRegion = panes.DatabaseResultsRegion
	databaseDetailsRegion = panes.DatabaseDetailsRegion
)

func (a *App) startDatabase(dir string) *tab {
	if dir == "" {
		dir = a.opts.Cwd
	}
	d := newDatabaseState()
	if d == nil {
		return nil
	}
	tb := &tab{id: newTabID(), dir: dir, pane: d}
	a.ensureDatabaseEditor(tb)
	a.tabs = append(a.tabs, tb)
	return tb
}

func (a *App) ensureDatabaseEditor(tb *tab) {
	d := tb.databaseState()
	if d.SQL != nil {
		return
	}
	d.SQL = &editor.State{Buffer: editor.NewBuffer(""), Placeholder: "SELECT * FROM …", Label: "Read-only SQL query", NoFolding: true, NoScrollPastEnd: true}
	d.SQL.Buffer.SetLanguage(editor.LanguageNamed("sql"))
	d.SQL.OnInput = func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputKeyDown && ev.Key == ui.KeyEnter && ev.Mods == ui.Cmd {
			a.runDatabaseQuery(tb)
			return true
		}
		return false
	}
	d.SQL.OnCompletion = func(req editor.CompletionRequest) {
		d.SQL.ShowCompletions(req.Offset-len(req.Prefix), databaseCompletions(d, req))
	}
}
func (a *App) openDatabase(dir string) { a.activate(a.startDatabase(dir)) }
func (a *App) openDatabaseProfile(name string) {
	for _, tb := range a.tabs {
		if tb.databaseState() != nil && tb.databaseState().Profile == name {
			a.activate(tb)
			return
		}
	}
	tb := a.startDatabase(a.currentDir())
	tb.databaseState().Profile = name
	if p, ok := a.databaseStore.Get(name); ok {
		tb.databaseState().Driver = p.Driver
	}
	a.activate(tb)
	a.loadDatabaseSchema(tb)
}
func (a *App) databaseWork(work func()) {
	if a.redraw == nil {
		work()
	} else {
		go work()
	}
}

type databaseRuntime interface {
	LoadSchema()
	OpenObject(database.Table)
	OpenStructure(database.Table)
	CopyCreationStatement(database.Table)
	CopyFunctionDefinition(database.Table)
	LoadFunction()
	LoadRows(bool)
	RunQuery()
	RunAnalyze(string)
	RunSQL(string, string, func(context.Context, string, string) (database.Result, error))
}

var newDatabaseState = func() *databaseState { return nil }
var newDatabaseRuntime = func(*App, *tab) databaseRuntime { return nil }

func (a *App) databaseRuntime(tb *tab) databaseRuntime { return newDatabaseRuntime(a, tb) }

func (a *App) loadDatabaseSchema(tb *tab) { a.databaseRuntime(tb).LoadSchema() }

func (a *App) openDatabaseObject(tb *tab, object database.Table) {
	a.databaseRuntime(tb).OpenObject(object)
}

func (a *App) openDatabaseStructure(tb *tab, object database.Table) {
	a.databaseRuntime(tb).OpenStructure(object)
}

func (a *App) databaseObjectMenu(m *ui.Menu, tb *tab, object database.Table) {
	dbHookDatabaseObjectMenu(a, m, tb, object)
}

func (a *App) copyDatabaseCreationStatement(tb *tab, object database.Table) {
	a.databaseRuntime(tb).CopyCreationStatement(object)
}

func (a *App) copyDatabaseFunctionDefinition(tb *tab, object database.Table) {
	a.databaseRuntime(tb).CopyFunctionDefinition(object)
}

func (a *App) loadDatabaseFunction(tb *tab) { a.databaseRuntime(tb).LoadFunction() }

func (a *App) loadDatabaseRows(tb *tab, activate bool) { a.databaseRuntime(tb).LoadRows(activate) }

func (a *App) runDatabaseQuery(tb *tab) { a.databaseRuntime(tb).RunQuery() }

func (a *App) runDatabaseAnalyze(tb *tab, query string) { a.databaseRuntime(tb).RunAnalyze(query) }

func (a *App) runDatabaseSQL(tb *tab, query, source string, run func(context.Context, string, string) (database.Result, error)) {
	a.databaseRuntime(tb).RunSQL(query, source, run)
}

func (a *App) beginDatabaseProfile(tb *tab, name string) {
	dbBeginProfile(tb.databaseState(), name, a.databaseStore)
}

func (a *App) draftDatabaseConfig(d *databaseState) (database.ConnectionConfig, error) {
	return dbDraftConfig(d, a.databaseStore)
}

func (a *App) testDatabaseConnection(tb *tab) {
	d := tb.databaseState()
	dbTestConnection(d, a.databaseStore, a.databaseWork, a.update, func() bool { return tb.databaseState() == d })
}

func (a *App) saveDatabaseProfile(tb *tab) {
	d := tb.databaseState()
	name, oldName, saved := dbSaveProfile(d, a.databaseStore)
	if !saved {
		return
	}
	for _, pane := range a.tabs {
		if pane.databaseState() == nil {
			continue
		}
		if pane == tb || oldName != "" && pane.databaseState().Profile == oldName {
			pane.databaseState().Profile = name
			for i := range pane.databaseState().Results {
				pane.databaseState().Results[i] = databaseResultView{SelectedRow: -1}
			}
			a.loadDatabaseSchema(pane)
		}
	}
}

func (a *App) deleteDatabaseProfile(tb *tab, name string) {
	if err := dbDeleteProfile(name, a.databaseStore); err != nil {
		tb.databaseState().Status = err.Error()
		return
	}
	for _, pane := range a.tabs {
		if d := pane.databaseState(); d != nil && d.Profile == name {
			d.ClearProfile()
		}
	}
	tb.databaseState().Deleting = false
}

func (a *App) databasePasswordField(c *ui.Context, d *databaseState, label string) {
	dbHookDatabasePasswordField(a, c, d, label)
}

var databaseRender panes.DatabaseRenderer = func(c *ui.Context, _ *ui.Theme, _ panes.DatabaseView) ui.Element { return ui.Box(c).Grow(1) }

func (a *App) databasePane(c *ui.Context, theme *ui.Theme, tb *tab) ui.Element {
	d := tb.databaseState()
	return databaseRender(c, theme, panes.DatabaseView{
		ID:     tb.id,
		Narrow: &d.Narrow, Compact: &d.Compact,
		LegacyEditorPercent: &d.LegacyEditorPercent, EditorHeight: &d.EditorHeight,
		BrowserWidth: &d.BrowserWidth, InspectorWidth: &d.InspectorWidth, Region: &d.Region,
		Editing:        func() bool { return d.Editing },
		ShowBrowser:    func() bool { return d.ShowBrowser },
		ShowDetails:    func() bool { return d.ShowDetails },
		HasSelectedRow: d.HasSelectedRow,
		Profile:        func() string { return d.Profile },
		ProfileExists:  func(name string) bool { _, ok := a.databaseStore.Get(name); return ok },
		StoreError:     a.databaseStore.Err,
		Header:         func() { a.databaseHeader(c, theme, tb) },
		Form:           func() { a.databaseProfileForm(c, theme, tb) },
		Empty:          func() { a.databaseEmpty(c, theme, tb) },
		Objects:        func() { a.databaseObjects(c, theme, tb) },
		Center:         func() { a.databaseCenter(c, theme, tb) },
		Inspector:      func() { a.databaseInspector(c, theme, tb) },
	})
}

func (a *App) databaseHeader(c *ui.Context, theme *ui.Theme, tb *tab) {
	dbHookDatabaseHeader(a, c, theme, tb)
}

func (a *App) databaseEmpty(c *ui.Context, theme *ui.Theme, tb *tab) {
	dbHookDatabaseEmpty(a, c, theme, tb)
}

func (a *App) databaseObjects(c *ui.Context, theme *ui.Theme, tb *tab) {
	dbHookDatabaseObjects(a, c, theme, tb)
}

func (a *App) databaseCenter(c *ui.Context, theme *ui.Theme, tb *tab) {
	dbHookDatabaseCenter(a, c, theme, tb)
}

func (a *App) databaseResults(c *ui.Context, theme *ui.Theme, tb *tab) {
	dbHookDatabaseResults(a, c, theme, tb)
}

func (a *App) databaseInspector(c *ui.Context, theme *ui.Theme, tb *tab) {
	dbHookDatabaseInspector(a, c, theme, tb)
}

func (a *App) databaseProfileForm(c *ui.Context, theme *ui.Theme, tb *tab) {
	dbHookDatabaseProfileForm(a, c, theme, tb)
}

func (a *App) databaseSidebar(c *ui.Context, theme *ui.Theme) { dbHookDatabaseSidebar(a, c, theme) }

var dbHookDatabaseObjectMenu = func(a *App, m *ui.Menu, tb *tab, object database.Table) {}

var dbHookDatabasePasswordField = func(a *App, c *ui.Context, d *databaseState, label string) {}

var dbHookDatabaseHeader = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {}

var dbHookDatabaseEmpty = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {}

var dbHookDatabaseObjects = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {}

var dbHookDatabaseCenter = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {}

var dbHookDatabaseResults = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {}

var dbHookDatabaseInspector = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {}

var dbHookDatabaseProfileForm = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {}

var dbHookDatabaseSidebar = func(a *App, c *ui.Context, theme *ui.Theme) {}

var dbBeginProfile = func(*databaseState, string, databaseStore) {}
var dbDraftConfig = func(*databaseState, databaseStore) (database.ConnectionConfig, error) {
	return database.ConnectionConfig{}, nil
}
var dbTestConnection = func(*databaseState, databaseStore, func(func()), func(func()), func() bool) {}
var dbSaveProfile = func(*databaseState, databaseStore) (string, string, bool) { return "", "", false }
var dbDeleteProfile = func(string, databaseStore) error { return nil }
