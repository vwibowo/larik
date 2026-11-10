package desktop

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"larik/internal/database"
)

type databaseState struct {
	ctx                                            context.Context
	cancel                                         context.CancelFunc
	seq                                            uint64
	profile                                        string
	driver                                         string
	emptyChoice                                    string
	tables                                         []database.Table
	schemaOpen                                     map[string]*bool
	schema, table                                  string
	sql                                            *editor.State
	functionEditor                                 *editor.State
	functionStatus                                 string
	showFunctionDefinition                         bool
	showStructure                                  bool
	exportSeq                                      uint64
	search                                         string
	pageSize                                       int
	results                                        [3]databaseResultView
	activeResult, region                           int
	loading                                        bool
	status                                         string
	editing, deleting                              bool
	oldName, draftName, draftDriver, draftValue    string
	draftHost, draftPort, draftDatabase, draftUser string
	draftPasswordEnv, draftPassword, draftSSLMode  string
	draftClearPassword                             bool
	draftTrust                                     bool
	draftAgent                                     bool
	testingConnection                              bool
	browserWidth, inspectorWidth, editorHeight     float32
	legacyEditorPercent                            float32
	showBrowser, showDetails                       bool
	narrow, compact                                bool
}

const (
	databaseData = iota
	databaseQuery
	databaseAgent
)

const (
	databaseTablesRegion = iota
	databaseResultsRegion
	databaseDetailsRegion
)

type databaseResultView struct {
	result      database.Result
	selectedRow int
	offset      int
	elapsed     time.Duration
	status      string
	source      string
	grid        ui.ListState
	selection   ui.Selection[int]
	focusGrid   bool
}

func (d *databaseState) currentResult() *databaseResultView { return &d.results[d.activeResult] }
func (d *databaseState) selectResult(index int) {
	d.activeResult = index
	d.region = databaseResultsRegion
}
func (d *databaseState) hasSelectedRow() bool {
	v := d.currentResult()
	return v.selectedRow >= 0 && v.selectedRow < len(v.result.Rows)
}

func databaseTiming(elapsed time.Duration) string {
	if elapsed == 0 {
		return "—"
	}
	if elapsed < time.Millisecond {
		return "<1 ms"
	}
	return elapsed.Round(time.Millisecond).String()
}

func (d *databaseState) close() {
	if d.cancel != nil {
		d.cancel()
	}
}

func (a *App) startDatabase(dir string) *tab {
	if dir == "" {
		dir = a.opts.Cwd
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &databaseState{ctx: ctx, cancel: cancel, draftDriver: "sqlite", browserWidth: 26, inspectorWidth: 76, editorHeight: 180, pageSize: database.DefaultRows, showBrowser: true, region: databaseResultsRegion}
	tb := &tab{id: newTabID(), dir: dir, database: d}
	a.ensureDatabaseEditor(tb)
	for i := range d.results {
		d.results[i].selectedRow = -1
	}
	a.tabs = append(a.tabs, tb)
	return tb
}

func (a *App) ensureDatabaseEditor(tb *tab) {
	d := tb.database
	if d.sql != nil {
		return
	}
	d.sql = &editor.State{Buffer: editor.NewBuffer(""), Placeholder: "SELECT * FROM …", Label: "Read-only SQL query", NoFolding: true, NoScrollPastEnd: true}
	d.sql.Buffer.SetLanguage(editor.LanguageNamed("sql"))
	d.sql.OnInput = func(ev ui.InputEvent) bool {
		if ev.Kind == ui.InputKeyDown && ev.Key == ui.KeyEnter && ev.Mods == ui.Ctrl {
			a.runDatabaseQuery(tb)
			return true
		}
		return false
	}
	d.sql.OnCompletion = func(req editor.CompletionRequest) {
		d.sql.ShowCompletions(req.Offset-len(req.Prefix), databaseCompletions(d, req))
	}
}
func (a *App) openDatabase(dir string) { a.activate(a.startDatabase(dir)) }
func (a *App) openDatabaseProfile(name string) {
	for _, tb := range a.tabs {
		if tb.database != nil && tb.database.profile == name {
			a.activate(tb)
			return
		}
	}
	tb := a.startDatabase(a.currentDir())
	tb.database.profile = name
	if p, ok := a.databaseStore.get(name); ok {
		tb.database.driver = p.Driver
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

func (a *App) loadDatabaseSchema(tb *tab) {
	d := tb.database
	if d == nil || d.profile == "" || a.databaseStore.manager == nil {
		return
	}
	if p, ok := a.databaseStore.get(d.profile); ok {
		d.driver = p.Driver
	}
	d.seq++
	seq, profile := d.seq, d.profile
	d.loading, d.status = true, "Loading schema…"
	a.databaseWork(func() {
		tables, err := a.databaseStore.manager.Schema(d.ctx, profile, false)
		a.update(func() {
			if tb.database != d || d.ctx.Err() != nil || d.seq != seq {
				return
			}
			d.loading = false
			if err != nil {
				d.status = err.Error()
				return
			}
			oldSchema, oldTable := d.schema, d.table
			d.tables, d.schema, d.table, d.status = tables, "", "", ""
			d.showStructure = false
			if len(tables) > 0 {
				d.schema, d.table = tables[0].Schema, tables[0].Name
				for _, table := range tables {
					if !database.IsFunction(table.Type) {
						d.schema, d.table = table.Schema, table.Name
						break
					}
				}
				for _, table := range tables {
					if table.Schema == oldSchema && table.Name == oldTable {
						d.schema, d.table = oldSchema, oldTable
						break
					}
				}
				if database.IsFunction(findDatabaseObjectType(tables, d.schema, d.table)) {
					d.showFunctionDefinition = true
					a.loadDatabaseFunction(tb)
				} else {
					d.showFunctionDefinition = false
					d.results[databaseData].offset = 0
					a.loadDatabaseRows(tb, false)
				}
			}
		})
	})
}

func findDatabaseObjectType(objects []database.Table, schema, name string) string {
	for _, object := range objects {
		if object.Schema == schema && object.Name == name {
			return object.Type
		}
	}
	return ""
}

func findDatabaseObject(objects []database.Table, schema, name string) (database.Table, bool) {
	for _, object := range objects {
		if object.Schema == schema && object.Name == name {
			return object, true
		}
	}
	return database.Table{}, false
}

func databaseObjectMarker(kind string) string {
	switch {
	case database.IsFunction(kind):
		return "ƒ"
	case strings.EqualFold(kind, "VIEW"):
		return "◫"
	default:
		return "▦"
	}
}

func (a *App) openDatabaseObject(tb *tab, object database.Table) {
	d := tb.database
	d.schema, d.table = object.Schema, object.Name
	d.showStructure = false
	if database.IsFunction(object.Type) {
		d.showFunctionDefinition = true
		a.loadDatabaseFunction(tb)
		return
	}
	d.showFunctionDefinition = false
	d.results[databaseData].offset = 0
	a.loadDatabaseRows(tb, true)
}

func (a *App) openDatabaseStructure(tb *tab, object database.Table) {
	d := tb.database
	d.schema, d.table = object.Schema, object.Name
	d.showFunctionDefinition = false
	d.showStructure = true
}

func (a *App) databaseObjectMenu(m *ui.Menu, tb *tab, object database.Table) {
	isFunction := database.IsFunction(object.Type)
	if isFunction {
		if m.Item("Open Definition").Chosen() {
			a.openDatabaseObject(tb, object)
		}
	} else {
		if m.Item("Open").Chosen() {
			a.openDatabaseObject(tb, object)
		}
		if m.Item("Open Structure").Chosen() {
			a.openDatabaseStructure(tb, object)
		}
	}
	m.Separator()
	if m.Item("Copy Name").Chosen() && a.copyText != nil {
		a.copyText(object.Name)
	}
	m.Submenu("Copy Script As", func(sub *ui.Menu) {
		if isFunction {
			if sub.Item("Function Definition").Chosen() {
				a.copyDatabaseFunctionDefinition(tb, object)
			}
			return
		}
		if sub.Item("SELECT statement").Chosen() && a.copyText != nil {
			a.copyText(database.SelectStatement(tb.database.driver, object.Schema, object.Name))
		}
		if sub.Item("Creation").Chosen() {
			a.copyDatabaseCreationStatement(tb, object)
		}
	})
	if !isFunction {
		m.Submenu("Export", func(sub *ui.Menu) {
			if sub.Item("CSV").Chosen() {
				a.exportDatabaseObject(tb, object)
			}
		})
	}
}

func (a *App) copyDatabaseCreationStatement(tb *tab, object database.Table) {
	d := tb.database
	if d == nil || d.profile == "" || a.databaseStore.manager == nil || a.copyText == nil {
		return
	}
	copyText := a.copyText
	profile := d.profile
	a.databaseWork(func() {
		statement, err := a.databaseStore.manager.CreationStatement(d.ctx, profile, object.Schema, object.Name, false)
		a.update(func() {
			if tb.database != d || d.ctx.Err() != nil {
				return
			}
			if err != nil {
				d.status = err.Error()
				return
			}
			copyText(statement)
			d.status = "Copied creation script"
		})
	})
}

func (a *App) copyDatabaseFunctionDefinition(tb *tab, object database.Table) {
	d := tb.database
	if d == nil || d.profile == "" || a.databaseStore.manager == nil || a.copyText == nil {
		return
	}
	copyText := a.copyText
	profile := d.profile
	a.databaseWork(func() {
		definition, err := a.databaseStore.manager.FunctionDefinition(d.ctx, profile, object.Schema, object.Name, false)
		a.update(func() {
			if tb.database != d || d.ctx.Err() != nil {
				return
			}
			if err != nil {
				d.status = err.Error()
				return
			}
			copyText(definition)
			d.status = "Copied function definition"
		})
	})
}

func (a *App) loadDatabaseFunction(tb *tab) {
	d := tb.database
	if d == nil || d.profile == "" || a.databaseStore.manager == nil {
		return
	}
	d.seq++
	seq, profile, schema, function := d.seq, d.profile, d.schema, d.table
	d.loading, d.functionStatus, d.functionEditor = true, "Loading function definition…", nil
	a.databaseWork(func() {
		definition, err := a.databaseStore.manager.FunctionDefinition(d.ctx, profile, schema, function, false)
		a.update(func() {
			if tb.database != d || d.ctx.Err() != nil || d.seq != seq {
				return
			}
			d.loading = false
			if err != nil {
				d.functionStatus = err.Error()
				return
			}
			buffer := editor.NewBuffer(definition)
			buffer.SetLanguage(editor.LanguageNamed("sql"))
			d.functionEditor = &editor.State{Buffer: buffer, ReadOnly: true, NoFolding: true, NoActiveLine: true, NoScrollPastEnd: true, Label: "Function definition"}
			d.functionStatus = ""
		})
	})
}

func (a *App) loadDatabaseRows(tb *tab, activate bool) {
	d := tb.database
	if d == nil || d.profile == "" || d.table == "" || a.databaseStore.manager == nil {
		return
	}
	d.seq++
	v := &d.results[databaseData]
	seq, profile, schema, table, offset, limit := d.seq, d.profile, d.schema, d.table, v.offset, d.pageSize
	if limit <= 0 {
		limit = database.DefaultRows
	}
	d.loading, d.status, v.status = true, "", "Loading rows…"
	if activate {
		d.selectResult(databaseData)
	}
	a.databaseWork(func() {
		start := time.Now()
		result, err := a.databaseStore.manager.Rows(d.ctx, profile, schema, table, offset, limit, false)
		a.update(func() {
			if tb.database != d || d.ctx.Err() != nil || d.seq != seq {
				return
			}
			d.loading, v.elapsed = false, time.Since(start)
			if err != nil {
				v.status = err.Error()
				return
			}
			v.result, v.selectedRow, v.status = result, -1, ""
			v.selection.Clear()
			v.grid.Cells = ui.ColumnRange{}
			v.source = schema + "." + table
		})
	})
}

func (a *App) runDatabaseQuery(tb *tab) {
	d := tb.database
	if d == nil || d.profile == "" || a.databaseStore.manager == nil {
		return
	}
	d.seq++
	v := &d.results[databaseQuery]
	seq, profile, query := d.seq, d.profile, d.sql.Buffer.String()
	d.loading, d.status, v.status = true, "", "Running query…"
	d.selectResult(databaseQuery)
	a.databaseWork(func() {
		start := time.Now()
		result, err := a.databaseStore.manager.Query(d.ctx, profile, query, database.DefaultRows, false)
		a.update(func() {
			if tb.database != d || d.ctx.Err() != nil || d.seq != seq {
				return
			}
			d.loading, v.elapsed = false, time.Since(start)
			if err != nil {
				v.status = err.Error()
				return
			}
			v.result, v.selectedRow, v.status = result, -1, ""
			v.selection.Clear()
			v.grid.Cells = ui.ColumnRange{}
			v.source = "SQL query"
		})
	})
}

func (a *App) beginDatabaseProfile(tb *tab, name string) {
	d := tb.database
	d.editing, d.deleting, d.oldName, d.draftName = true, false, name, name
	d.draftDriver, d.draftValue, d.draftAgent, d.status = "sqlite", "", false, ""
	d.draftHost, d.draftPort, d.draftDatabase, d.draftUser, d.draftTrust = "", "1433", "", "", false
	d.draftPasswordEnv, d.draftPassword, d.draftSSLMode, d.draftClearPassword = "", "", "prefer", false
	if p, ok := a.databaseStore.get(name); ok {
		d.draftDriver, d.draftAgent = p.Driver, p.AgentAccess
		switch p.Driver {
		case "sqlite":
			d.draftValue = p.Path
		case "postgres":
			d.draftPasswordEnv = p.PasswordEnv
			d.draftSSLMode = p.SSLMode
			if d.draftSSLMode == "" {
				d.draftSSLMode = "prefer"
			}
			if p.URLEnv == "" {
				d.draftHost, d.draftPort, d.draftDatabase, d.draftUser = p.Host, strconv.Itoa(p.Port), p.Database, p.User
			} else {
				d.draftValue = p.URLEnv // legacy environment-backed URL
			}
		case "mysql":
			d.draftValue = p.DSNEnv
			d.draftHost, d.draftPort, d.draftDatabase, d.draftUser = p.Host, strconv.Itoa(p.Port), p.Database, p.User
			d.draftPasswordEnv, d.draftSSLMode = p.PasswordEnv, p.SSLMode
			if d.draftSSLMode == "" {
				d.draftSSLMode = "required"
			}
		case "sqlserver":
			d.draftPasswordEnv = p.PasswordEnv
			d.draftHost, d.draftPort, d.draftDatabase, d.draftUser, d.draftTrust = p.Host, strconv.Itoa(p.Port), p.Database, p.User, p.TrustServerCertificate
		}
	}
}

func (a *App) draftDatabaseConfig(d *databaseState) (database.ConnectionConfig, error) {
	p := database.ConnectionConfig{Driver: d.draftDriver, AgentAccess: d.draftAgent}
	value := strings.TrimSpace(d.draftValue)
	switch p.Driver {
	case "sqlite":
		p.Path = value
	case "postgres":
		if value != "" { // preserve an existing URL-based profile
			p.URLEnv = value
			break
		}
		port, err := strconv.Atoi(strings.TrimSpace(d.draftPort))
		if err != nil {
			return p, fmt.Errorf("enter a valid PostgreSQL port")
		}
		p.Host, p.Port, p.Database, p.User, p.SSLMode = strings.TrimSpace(d.draftHost), port, strings.TrimSpace(d.draftDatabase), strings.TrimSpace(d.draftUser), d.draftSSLMode
		a.applyDraftDatabasePassword(d, &p)
	case "mysql":
		if value != "" { // legacy DSN profiles remain supported
			p.DSNEnv = value
			break
		}
		port, err := strconv.Atoi(strings.TrimSpace(d.draftPort))
		if err != nil {
			return p, fmt.Errorf("enter a valid MySQL port")
		}
		p.Host, p.Port, p.Database, p.User, p.SSLMode = strings.TrimSpace(d.draftHost), port, strings.TrimSpace(d.draftDatabase), strings.TrimSpace(d.draftUser), d.draftSSLMode
		a.applyDraftDatabasePassword(d, &p)
	case "sqlserver":
		port, err := strconv.Atoi(strings.TrimSpace(d.draftPort))
		if err != nil {
			return p, fmt.Errorf("enter a valid SQL Server port")
		}
		p.Host, p.Port, p.Database, p.User, p.TrustServerCertificate = strings.TrimSpace(d.draftHost), port, strings.TrimSpace(d.draftDatabase), strings.TrimSpace(d.draftUser), d.draftTrust
		p.PasswordEnv = strings.TrimSpace(d.draftPasswordEnv)
		a.applyDraftDatabasePassword(d, &p)
	}
	if _, err := database.New(map[string]database.ConnectionConfig{"connection-test": p}, "/"); err != nil {
		return p, err
	}
	return p, nil
}

func (a *App) applyDraftDatabasePassword(d *databaseState, p *database.ConnectionConfig) {
	if d.draftClearPassword {
		p.Password, p.PasswordError, p.HasPassword = "", "", false
	}
	if d.draftPassword != "" {
		p.Password, p.PasswordError, p.HasPassword, p.PasswordEnv = d.draftPassword, "", true, ""
		return
	}
	if p.PasswordEnv != "" {
		p.Password, p.PasswordError, p.HasPassword = "", "", false
		return
	}
	if !d.draftClearPassword && d.oldName != "" {
		if existing, ok := a.databaseStore.get(d.oldName); ok && existing.HasPassword {
			p.Password, p.PasswordError, p.HasPassword = existing.Password, existing.PasswordError, true
		}
	}
}

func (a *App) testDatabaseConnection(tb *tab) {
	d := tb.database
	p, err := a.draftDatabaseConfig(d)
	if err != nil {
		d.status = err.Error()
		return
	}
	d.testingConnection, d.status = true, "Testing connection…"
	a.databaseWork(func() {
		manager, err := database.New(map[string]database.ConnectionConfig{"connection-test": p}, "/")
		if err == nil {
			_, err = manager.Schema(context.Background(), "connection-test", false)
		}
		if err != nil {
			err = fmt.Errorf("Connection failed: %w", err)
		}
		a.update(func() {
			if tb.database != d || d.ctx.Err() != nil {
				return
			}
			d.testingConnection = false
			if err != nil {
				d.status = err.Error()
			} else {
				d.status = "Connection successful"
			}
		})
	})
}

func (a *App) saveDatabaseProfile(tb *tab) {
	d := tb.database
	name := strings.TrimSpace(d.draftName)
	if name == "" {
		d.status = "Connection name is required"
		return
	}
	next := a.databaseStore.all()
	if _, exists := next[name]; exists && name != d.oldName {
		d.status = "Connection name already exists"
		return
	}
	delete(next, d.oldName)
	p, err := a.draftDatabaseConfig(d)
	if err != nil {
		d.status = err.Error()
		return
	}
	next[name] = p
	if err := a.databaseStore.save(next); err != nil {
		d.status = err.Error()
		return
	}
	d.editing, d.profile, d.driver, d.status = false, name, p.Driver, ""
	d.draftPassword = ""
	for _, pane := range a.tabs {
		if pane.database == nil {
			continue
		}
		if pane == tb || d.oldName != "" && pane.database.profile == d.oldName {
			pane.database.profile = name
			for i := range pane.database.results {
				pane.database.results[i] = databaseResultView{selectedRow: -1}
			}
			a.loadDatabaseSchema(pane)
		}
	}
}

func (a *App) deleteDatabaseProfile(tb *tab, name string) {
	next := a.databaseStore.all()
	delete(next, name)
	if err := a.databaseStore.save(next); err != nil {
		tb.database.status = err.Error()
		return
	}
	for _, pane := range a.tabs {
		if d := pane.database; d != nil && d.profile == name {
			d.seq++
			d.profile, d.schema, d.table, d.status = "", "", "", ""
			d.tables = nil
			for i := range d.results {
				d.results[i] = databaseResultView{selectedRow: -1}
			}
		}
	}
	tb.database.deleting = false
}

func (a *App) databasePasswordField(c *ui.Context, d *databaseState, label string) {
	ui.Text(c, label).FontSize(12).Bold()
	ui.TextInput(c.Key("db-profile-password"), &d.draftPassword).Placeholder("Enter password").Label(label).FillWidth().Password()
	if d.oldName == "" {
		return
	}
	p, ok := a.databaseStore.get(d.oldName)
	if !ok || !p.HasPassword {
		return
	}
	if d.draftClearPassword {
		ui.Text(c, "Saved password will be removed when you save.").FontSize(11).TextColor(c.Theme().TextMuted)
		return
	}
	ui.Text(c, "A password is saved securely. Leave this blank to keep it.").FontSize(11).TextColor(c.Theme().TextMuted)
	if ui.Button(c.Key("db-profile-remove-password"), "Remove saved password").TextColor(c.Theme().Danger).Clicked() {
		d.draftClearPassword, d.draftPassword = true, ""
	}
}

func (a *App) databasePane(c *ui.Context, theme *ui.Theme, tb *tab) ui.Element {
	d := tb.database
	pane := ui.Column(c.Key(tb.id + "-database")).Grow(1).MinWidth(0).Focusable()
	if bounds := pane.Bounds(); bounds.W > 0 {
		d.narrow, d.compact = bounds.W < 600, bounds.W < 980
		if d.legacyEditorPercent > 0 && bounds.H > 0 {
			if d.legacyEditorPercent == 34 {
				d.editorHeight = 180
			} else {
				d.editorHeight = max(120, min(bounds.H*d.legacyEditorPercent/100, 320))
			}
			d.legacyEditorPercent = 0
		}
		if bounds.H > 0 {
			d.editorHeight = max(120, min(d.editorHeight, bounds.H*0.70))
		}
	}
	return pane.Children(func() {
		a.databaseHeader(c, theme, tb)
		if d.editing {
			a.databaseProfileForm(c, theme, tb)
			return
		}
		if a.databaseStore.err != nil {
			ui.Text(c, "Cannot load saved connections: "+a.databaseStore.err.Error()).Padding(16).TextColor(theme.Danger)
			return
		}
		if d.profile == "" {
			a.databaseEmpty(c, theme, tb)
			return
		}
		if _, ok := a.databaseStore.get(d.profile); !ok {
			ui.Text(c, "Connection removed. Choose another from the sidebar.").Padding(16)
			return
		}
		if d.compact {
			ui.Tabs(c.Key("db-regions"), &d.region, "Tables", "Results", "Details").Label("Database region")
			switch d.region {
			case databaseTablesRegion:
				a.databaseObjects(c, theme, tb)
			case databaseDetailsRegion:
				a.databaseInspector(c, theme, tb)
			default:
				a.databaseCenter(c, theme, tb)
			}
			return
		}
		main := func() { a.databaseCenter(c, theme, tb) }
		if d.showBrowser {
			center := main
			main = func() {
				ui.SplitPercent(c, &d.browserWidth, func() { a.databaseObjects(c, theme, tb) }, center).Grow(1)
			}
		}
		if d.showDetails && d.hasSelectedRow() {
			ui.SplitPercent(c, &d.inspectorWidth, main, func() { a.databaseInspector(c, theme, tb) }).Grow(1)
		} else {
			main()
		}
	})
}

func (a *App) databaseHeader(c *ui.Context, theme *ui.Theme, tb *tab) {
	d := tb.database
	p, _ := a.databaseStore.get(d.profile)
	ui.Row(c).Height(48).Gap(8).Padding(7, 14).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
		ui.Icon(c, iconDatabase).FontSize(17).TextColor(theme.Accent)
		name := "Database Manager"
		if d.profile != "" {
			name = d.profile
		}
		ui.Text(c, name).FontSize(14).Bold().Grow(1).Shrink(1).SingleLine()
		if d.editing {
			return
		}
		if p.Driver != "" {
			driver := databaseDriverLabel(p.Driver)
			ui.Text(c, driver+" · Read only").FontSize(11).TextColor(theme.TextMuted).SingleLine()
		}
		if d.profile != "" {
			if !d.compact {
				browserTip := "Show tables on the left"
				if d.showBrowser {
					browserTip = "Hide tables on the left"
				}
				a.iconButton(c, theme, "db-objects-toggle", iconPanel, browserTip, d.showBrowser, func() {
					d.showBrowser = !d.showBrowser
				})
				if d.hasSelectedRow() {
					detailsTip := "Show details on the right"
					if d.showDetails {
						detailsTip = "Hide details on the right"
					}
					a.iconButton(c, theme, "db-details-toggle", iconPanelRight, detailsTip, d.showDetails, func() {
						d.showDetails = !d.showDetails
					})
				} else {
					const tip = "Select a row to show details on the right"
					ui.Box(c.Key("db-details-toggle")).Padding(6).Radius(6).Label(tip).Tooltip(tip).Disabled(true).Children(func() {
						ui.Icon(c, iconPanelRight).FontSize(15).TextColor(theme.TextMuted)
					})
				}
			}
		} else if ui.Button(c.Key("db-add"), "Add connection").Clicked() {
			a.beginDatabaseProfile(tb, "")
		}
	})
}

func databaseDriverLabel(driver string) string {
	switch driver {
	case "postgres":
		return "PostgreSQL"
	case "sqlserver":
		return "SQL Server"
	case "mysql":
		return "MySQL / MariaDB"
	default:
		return "SQLite"
	}
}

func (a *App) databaseEmpty(c *ui.Context, theme *ui.Theme, tb *tab) {
	names := a.databaseStore.names()
	ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
		ui.Icon(c, iconDatabase).FontSize(34).TextColor(theme.TextMuted)
		ui.Text(c, "Choose a database connection").FontSize(16).Bold()
		ui.Text(c, "Saved connections are in the Databases sidebar section.").FontSize(12).TextColor(theme.TextMuted)
		if len(names) > 0 {
			if tb.database.emptyChoice == "" {
				tb.database.emptyChoice = names[0]
			}
			ui.Select(c.Key("db-empty-choice"), &tb.database.emptyChoice, names).Width(250).Label("Database connection")
			if ui.Button(c.Key("db-open-choice"), "Open connection").Clicked() {
				tb.database.profile = tb.database.emptyChoice
				a.loadDatabaseSchema(tb)
			}
		} else if ui.Button(c.Key("db-empty-add"), "Add connection").Clicked() {
			a.beginDatabaseProfile(tb, "")
		}
	})
}

func (a *App) databaseObjects(c *ui.Context, theme *ui.Theme, tb *tab) {
	d := tb.database
	search := strings.ToLower(strings.TrimSpace(d.search))
	schemas := make([]string, 0)
	visible := make(map[string][]database.Table)
	for _, table := range d.tables {
		if search == "" || strings.Contains(strings.ToLower(table.Schema), search) || strings.Contains(strings.ToLower(table.Name), search) {
			if _, ok := visible[table.Schema]; !ok {
				schemas = append(schemas, table.Schema)
			}
			visible[table.Schema] = append(visible[table.Schema], table)
		}
	}
	ui.Column(c).Grow(1).MinWidth(0).BorderWidth(0, 1, 0, 0).BorderColor(theme.Border).Children(func() {
		ui.Row(c).Height(40).Gap(6).Padding(0, 8, 0, 12).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			ui.Icon(c, iconDatabase).FontSize(14).TextColor(theme.Accent)
			ui.Text(c, "Objects").FontSize(12).Bold().Grow(1)
			a.iconButton(c, theme, "db-refresh", iconReload, "Refresh database objects ("+keyText("Cmd+R")+")", false, func() {
				if !d.loading {
					a.loadDatabaseSchema(tb)
				}
			})
		})
		ui.Row(c).Padding(7, 9).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			ui.SearchField(c.Key("db-items-search"), &d.search).Label("Find database objects").Placeholder("Find tables, views, functions").Grow(1).MinWidth(0).
				OnShortcut(0, ui.KeyEscape, func() { d.search = "" })
		})
		if search != "" {
			matches := 0
			for _, tables := range visible {
				matches += len(tables)
			}
			ui.Text(c, fmt.Sprintf("%d of %d objects", matches, len(d.tables))).FontSize(11).TextColor(theme.TextMuted).Padding(7, 12)
		}
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Tree(c, func() {
				for _, schema := range schemas {
					if d.schemaOpen == nil {
						d.schemaOpen = map[string]*bool{}
					}
					open := d.schemaOpen[schema]
					if open == nil {
						initial := true
						open = &initial
						d.schemaOpen[schema] = open
					}
					if search != "" {
						*open = true
					}
					name := schema
					if name == "" {
						name = "Default"
					}
					item := ui.TreeItem(c.Key("db-schema-"+schema), name, open, func() {
						for _, table := range visible[schema] {
							label := databaseObjectMarker(table.Type) + "  " + table.Name
							row := ui.TreeItem(c.Key("db-table-"+table.Schema+"."+table.Name), label, nil, nil).Selected(d.schema == table.Schema && d.table == table.Name)
							if row.Clicked() {
								a.openDatabaseObject(tb, table)
							}
							row.ContextMenu(func(m *ui.Menu) { a.databaseObjectMenu(m, tb, table) })
						}
					})
					if item.Clicked() {
						*open = true
					}
				}
			}).Padding(6, 8)
			if len(schemas) == 0 && !d.loading {
				message := "No tables, views, or functions in this connection."
				if search != "" {
					message = "No objects match this search."
				}
				ui.Text(c, message).Padding(12).TextColor(theme.TextMuted)
			} else if len(schemas) == 0 && d.loading {
				ui.Text(c, "Loading database objects…").Padding(12).TextColor(theme.TextMuted)
			}
		})
	})
}

func (a *App) databaseCenter(c *ui.Context, theme *ui.Theme, tb *tab) {
	d := tb.database
	a.ensureDatabaseEditor(tb)
	if d.showStructure {
		object, ok := findDatabaseObject(d.tables, d.schema, d.table)
		ui.Column(c).Grow(1).MinHeight(100).Children(func() {
			ui.Row(c).Height(36).Gap(10).Padding(2, 14).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
				a.iconButton(c, theme, "db-structure-back", iconBack, "Back to table rows", false, func() {
					if ok {
						a.openDatabaseObject(tb, object)
					} else {
						d.showStructure = false
					}
				})
				ui.Text(c, "Structure").FontSize(12).Bold()
				ui.Text(c, d.schema+"."+d.table).FontSize(11).TextColor(theme.TextMuted).Grow(1).SingleLine()
				if ok {
					ui.Text(c, fmt.Sprintf("%d columns", len(object.Columns))).FontSize(11).TextColor(theme.TextMuted)
				}
			})
			if !ok {
				ui.Text(c, "Object structure is unavailable. Refresh the schema and try again.").Grow(1).Padding(16).TextColor(theme.TextMuted)
				return
			}
			ui.Row(c).Gap(16).Padding(8, 14).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
				ui.Text(c, "Column").FontSize(11).Bold().Grow(1)
				ui.Text(c, "Type").FontSize(11).Bold().Width(180)
				ui.Text(c, "Nullable").FontSize(11).Bold().Width(90)
				ui.Text(c, "Key").FontSize(11).Bold().Width(90)
			})
			ui.Scroll(c).Grow(1).Children(func() {
				for i, column := range object.Columns {
					ui.Row(c.Key(fmt.Sprintf("db-structure-column-%d", i))).Gap(16).Padding(8, 14).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
						ui.Text(c, column.Name).FontSize(12).Grow(1).SingleLine()
						ui.Text(c, column.Type).FontSize(11).TextColor(theme.TextMuted).Width(180).SingleLine()
						nullable := "No"
						if column.Nullable {
							nullable = "Yes"
						}
						ui.Text(c, nullable).FontSize(11).TextColor(theme.TextMuted).Width(90)
						key := "—"
						if column.PrimaryKey {
							key = "Primary"
						}
						ui.Text(c, key).FontSize(11).TextColor(theme.TextMuted).Width(90)
					})
				}
				if len(object.Columns) == 0 {
					ui.Text(c, "No columns are available for this object.").Padding(14).TextColor(theme.TextMuted)
				}
			})
		})
		return
	}
	if d.showFunctionDefinition {
		ui.Column(c).Grow(1).MinHeight(100).Children(func() {
			ui.Row(c).Height(36).Gap(10).Padding(2, 14).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
				a.iconButton(c, theme, "db-function-back", iconBack, "Back to SQL query", false, func() {
					d.showFunctionDefinition = false
				})
				ui.Text(c, "ƒ").FontSize(16).TextColor(theme.Accent)
				ui.Text(c, "Function definition").FontSize(12).Bold()
				ui.Text(c, d.schema+"."+d.table).FontSize(11).TextColor(theme.TextMuted).Grow(1).SingleLine()
				if d.functionEditor != nil {
					a.iconButton(c, theme, "db-copy-function", iconCopy, "Copy function definition", false, func() {
						c.Services().WriteClipboard(d.functionEditor.Buffer.String())
					})
				}
			})
			if d.functionStatus != "" {
				color := theme.Danger
				if d.loading {
					color = theme.TextMuted
				}
				ui.Text(c, d.functionStatus).Grow(1).Padding(16).TextColor(color)
				return
			}
			if d.functionEditor == nil {
				ui.Text(c, "Function definition is unavailable.").Grow(1).Padding(16).TextColor(theme.TextMuted)
				return
			}
			codeTheme := editor.DefaultTheme(theme)
			codeTheme.Background = theme.Background
			if a.paneOpacity < 1 {
				codeTheme.Background = theme.Background.Alpha(0)
			}
			codeTheme.Tokens[editor.TokenKeyword].Color = theme.Accent
			codeTheme.Tokens[editor.TokenString].Color = theme.Success
			codeTheme.Tokens[editor.TokenComment].Color = theme.TextMuted
			d.functionEditor.Theme = codeTheme
			editor.View(c.Key("db-function-definition"), d.functionEditor).Grow(1).MinWidth(0).MinHeight(0)
		})
		return
	}
	ui.SplitVertical(c, &d.editorHeight, func() {
		ui.Column(c).Grow(1).MinHeight(100).Children(func() {
			ui.Row(c).Height(36).Gap(10).Padding(2, 14).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "SQL query").FontSize(12).Bold().Grow(1)
				if !d.narrow {
					ui.Text(c, "Ctrl+Enter").FontSize(10).TextColor(theme.TextMuted).SingleLine()
				}
				run := ui.Box(c.Key("db-run")).Size(32, 32).Center().Focusable().Radius(6).Background(theme.Accent).Label("Run query (Ctrl+Enter)").Tooltip("Run query (Ctrl+Enter)").Disabled(d.loading).Children(func() {
					ui.Icon(c, iconRun).FontSize(15).TextColor(theme.AccentText)
				})
				if run.Clicked() {
					a.runDatabaseQuery(tb)
				}
			})
			codeTheme := editor.DefaultTheme(theme)
			codeTheme.Background = theme.Background
			if a.paneOpacity < 1 {
				codeTheme.Background = theme.Background.Alpha(0)
			}
			codeTheme.Tokens[editor.TokenKeyword].Color = theme.Accent
			codeTheme.Tokens[editor.TokenString].Color = theme.Success
			codeTheme.Tokens[editor.TokenComment].Color = theme.TextMuted
			d.sql.Theme = codeTheme
			editor.View(c.Key("db-sql"), d.sql).Grow(1).MinWidth(0).MinHeight(0)
		})
	}, func() { a.databaseResults(c, theme, tb) }).Grow(1)
}

func (a *App) databaseResults(c *ui.Context, theme *ui.Theme, tb *tab) {
	d := tb.database
	v := d.currentResult()
	r := v.result
	ui.Column(c).Grow(1).MinHeight(100).Children(func() {
		ui.Row(c).Height(36).Gap(12).Padding(4, 14).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			ui.Tabs(c.Key("db-result-tabs"), &d.activeResult, "Data", "Query", "Agent").Label("Database results")
			if v.source != "" {
				ui.Text(c, v.source).FontSize(11).TextColor(theme.TextMuted).Grow(1).SingleLine()
			}
			if len(r.Columns) > 0 {
				copyButton := ui.Box(c.Key("db-copy-menu")).Size(32, 32).Center().Focusable().Radius(6).Label("Copy selected cells").Tooltip("Copy selected cells as TSV").Disabled(v.selection.Len() == 0).Children(func() {
					ui.Icon(c, iconCopy).FontSize(15).TextColor(theme.Text)
				})
				copyButton.PopoverMenuButton(func(m *ui.Menu) {
					for _, item := range []struct {
						label   string
						headers bool
					}{{"Copy selection", false}, {"Copy with headers", true}} {
						if m.Item(item.label).Chosen() {
							if selected := databaseTSV(v, item.headers); selected != "" {
								c.Services().WriteClipboard(selected)
							}
						}
					}
				})
				exportTip := "Export loaded rows as CSV"
				if r.Truncated || r.ValuesTruncated {
					exportTip += " (partial result or shortened values)"
				}
				a.iconButton(c, theme, "db-export", iconExport, exportTip, false, func() { a.exportDatabaseResult(tb) })
			}
		})
		if len(r.Columns) == 0 {
			message := "Select a table to browse its rows."
			switch d.activeResult {
			case databaseQuery:
				message = "Run a read-only SQL query to see its results."
			case databaseAgent:
				message = "Agent results appear here when access is enabled."
			}
			ui.Text(c, message).Grow(1).Padding(16).TextColor(theme.TextMuted)
		} else {
			cols := make([]ui.TableColumn, len(r.Columns)+1)
			cols[0] = ui.TableColumn{ID: "_row", Title: "#", Width: 46, Fixed: true, RowHeader: true}
			for i, name := range r.Columns {
				align := ui.Start
				for _, row := range r.Rows {
					if i >= len(row) || row[i] == nil {
						continue
					}
					switch row[i].(type) {
					case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
						align = ui.End
					}
					break
				}
				cols[i+1] = ui.TableColumn{ID: fmt.Sprintf("%d", i), Title: name, Width: 170, MinWidth: 90, Align: align}
			}
			v.grid.Selected = &v.selectedRow
			v.grid.Selection = &v.selection
			v.grid.CellSelect = true
			v.grid.Label = func(row int) string { return fmt.Sprintf("Row %d", v.offset+row+1) }
			v.grid.RowBackground = func(row int) ui.Color {
				if row%2 == 1 {
					return theme.Text.Alpha(0.035)
				}
				return ui.Transparent
			}
			gridTheme := *theme
			gridTheme.Spacing = 3
			gridTheme.SurfaceHover = theme.Text.Alpha(0.08)
			if a.paneOpacity < 1 {
				gridTheme.Background = theme.Background.Alpha(0)
				gridTheme.InverseText = theme.Background
			}
			ui.Themed(c, &gridTheme).Grow(1).MinWidth(0).Children(func() {
				grid := ui.Table(c.Key(fmt.Sprintf("db-grid-%d", d.activeResult)), &v.grid, cols, len(r.Rows), func(row, col int) {
					if col == 0 {
						ui.Text(c, fmt.Sprint(v.offset+row+1)).FontSize(10).TextColor(theme.TextMuted).SingleLine()
						return
					}
					col--
					value := "NULL"
					muted := true
					if col < len(r.Rows[row]) && r.Rows[row][col] != nil {
						value, muted = fmt.Sprint(r.Rows[row][col]), false
					}
					cell := ui.Text(c, value).FontSize(11).SingleLine().Tooltip(value)
					if v.grid.CellSelected(row, col+1) {
						cell.TextColor(theme.AccentText)
					} else if muted {
						cell.TextColor(theme.TextMuted)
					}
				}).Grow(1)
				grid.OnShortcut(ui.Cmd, ui.KeyC, func() {
					if selected := databaseTSV(v, false); selected != "" {
						c.Services().WriteClipboard(selected)
					}
				})
				if v.focusGrid && v.grid.Focus(c) {
					v.focusGrid = false
				}
				if grid.Submitted() && d.hasSelectedRow() {
					v.focusGrid = !d.compact
					d.showDetails = true
					if d.compact {
						d.region = databaseDetailsRegion
					}
				}
			})
		}
		ui.Row(c).Height(32).Gap(8).Padding(4, 14).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).Children(func() {
			if v.status != "" || d.status != "" {
				status := v.status
				if d.status != "" {
					status = d.status
				}
				color := theme.Danger
				if d.loading || strings.HasPrefix(status, "Exported ") {
					color = theme.TextMuted
				}
				ui.Text(c, status).FontSize(11).TextColor(color).Grow(1).SingleLine()
			} else {
				label := fmt.Sprintf("%d rows · %s", len(r.Rows), databaseTiming(v.elapsed))
				if d.activeResult == databaseData && len(r.Rows) > 0 {
					label = fmt.Sprintf("Rows %d–%d · %s", v.offset+1, v.offset+len(r.Rows), databaseTiming(v.elapsed))
				}
				if r.Truncated {
					if d.activeResult == databaseData && r.NextOffset > 0 {
						label += " · More rows available"
					} else {
						label += " · Truncated at row or 1 MiB limit"
					}
				}
				if r.ValuesTruncated {
					label += " · Some values shortened"
				}
				ui.Text(c, label).FontSize(11).TextColor(theme.TextMuted).Grow(1).SingleLine()
			}
			if d.activeResult == databaseData {
				if d.pageSize <= 0 {
					d.pageSize = database.DefaultRows
				}
				pageSize := fmt.Sprint(d.pageSize)
				if ui.Select(c.Key("db-page-size"), &pageSize, []string{"25", "50", "100", "250", "500"}).Width(72).Label("Rows per page").Changed() {
					fmt.Sscan(pageSize, &d.pageSize)
					v.offset = 0
					a.loadDatabaseRows(tb, true)
				}
				prev := ui.Box(c.Key("db-prev")).Size(32, 32).Center().Focusable().Radius(6).Label("Previous page").Tooltip("Previous page").Disabled(d.loading || v.offset == 0).Children(func() {
					ui.Icon(c, iconBack).FontSize(15).TextColor(theme.Text)
				})
				if prev.Clicked() {
					v.offset = max(0, v.offset-d.pageSize)
					a.loadDatabaseRows(tb, true)
				}
				next := ui.Box(c.Key("db-next")).Size(32, 32).Center().Focusable().Radius(6).Label("Next page").Tooltip("Next page").Disabled(d.loading || r.NextOffset == 0).Children(func() {
					ui.Icon(c, iconForward).FontSize(15).TextColor(theme.Text)
				})
				if next.Clicked() {
					v.offset = r.NextOffset
					a.loadDatabaseRows(tb, true)
				}
			}
		})
	})
}

func (a *App) databaseInspector(c *ui.Context, theme *ui.Theme, tb *tab) {
	d := tb.database
	v := d.currentResult()
	ui.Column(c).Grow(1).MinWidth(0).BorderWidth(0, 0, 0, 1).BorderColor(theme.Border).Children(func() {
		ui.Row(c).Height(38).Padding(8, 14).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Details").FontSize(12).Bold().Grow(1)
			if d.compact {
				a.iconButton(c, theme, "db-details-back", iconBack, "Back to results", false, func() { d.region = databaseResultsRegion })
			}
		})
		if !d.hasSelectedRow() {
			ui.Text(c, "Select a row to inspect its fields.").Padding(12).TextColor(theme.TextMuted)
			return
		}
		row := v.result.Rows[v.selectedRow]
		ui.Scroll(c).Grow(1).Children(func() {
			for i, name := range v.result.Columns {
				value := "NULL"
				if i < len(row) && row[i] != nil {
					value = fmt.Sprint(row[i])
				}
				ui.Column(c.Key(fmt.Sprintf("db-field-%d", i))).Padding(8, 12).Gap(3).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Children(func() {
						ui.Text(c, name).FontSize(10).TextColor(theme.TextMuted).Grow(1)
						a.iconButton(c, theme, fmt.Sprintf("db-copy-field-%d", i), iconCopy, "Copy "+name, false, func() { c.Services().WriteClipboard(value) })
					})
					ui.Text(c, value).FontSize(11).MaxLines(8).Tooltip(value).Selectable()
				})
			}
		})
	})
}

func (a *App) databaseProfileForm(c *ui.Context, theme *ui.Theme, tb *tab) {
	d := tb.database
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(580).MaxWidthPercent(100).Padding(24, 28, 32, 28).Gap(22).Children(func() {
				title := "Add connection"
				if d.oldName != "" {
					title = "Edit connection"
				}
				ui.Column(c).Gap(5).Children(func() {
					ui.Text(c, title).FontSize(18).Bold()
					ui.Text(c, "Saved connections are available in every Desktop project.").FontSize(12).TextColor(theme.TextMuted)
				})
				ui.Column(c).Gap(7).FillWidth().Children(func() {
					ui.Text(c, "Name").FontSize(12).Bold()
					ui.TextInput(c.Key("db-profile-name"), &d.draftName).Placeholder("e.g. Analytics").Label("Connection name").FillWidth()
				})
				ui.Column(c).Gap(8).FillWidth().Children(func() {
					ui.Text(c, "Database type").FontSize(12).Bold()
					kind := 0
					drivers := []string{"sqlite", "postgres", "mysql", "sqlserver"}
					for i, driver := range drivers {
						if d.draftDriver == driver {
							kind = i
						}
					}
					if ui.Segmented(c.Key("db-profile-driver"), &kind, "SQLite", "PostgreSQL", "MySQL", "SQL Server").Label("Database type").AlignSelf(ui.Start).Changed() {
						d.draftDriver = drivers[kind]
						d.draftValue, d.draftPassword, d.draftPasswordEnv = "", "", ""
						d.draftClearPassword = false
						d.draftSSLMode = ""
						port := "1433"
						if d.draftDriver == "postgres" {
							port = "5432"
							d.draftSSLMode = "prefer"
						} else if d.draftDriver == "mysql" {
							port, d.draftSSLMode = "3306", "required"
						}
						d.draftHost, d.draftPort, d.draftDatabase, d.draftUser, d.draftTrust = "", port, "", "", false
						d.draftPasswordEnv = ""
					}
				})
				ui.Column(c).Gap(7).FillWidth().Children(func() {
					switch d.draftDriver {
					case "postgres":
						if d.draftValue != "" {
							ui.Text(c, "Legacy URL environment variable").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-value"), &d.draftValue).Placeholder("e.g. DATABASE_URL").Label("Connection URL environment variable").FillWidth()
							ui.Text(c, "This saved connection uses a legacy URL variable. Clear it to switch to the form fields.").FontSize(11).TextColor(theme.TextMuted)
						} else {
							ui.Text(c, "Server").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-host"), &d.draftHost).Placeholder("db.example.com").Label("Host").FillWidth()
							ui.Text(c, "Port").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-port"), &d.draftPort).Label("Port").Width(120)
							ui.Text(c, "Database").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-database"), &d.draftDatabase).Label("Database name").FillWidth()
							ui.Text(c, "Username").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-user"), &d.draftUser).Label("Username").FillWidth()
							a.databasePasswordField(c, d, "PostgreSQL password")
							ui.Text(c, "Password environment variable (optional)").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-password-env"), &d.draftPasswordEnv).Placeholder("e.g. DATABASE_PASSWORD").Label("Password environment variable").FillWidth()
							ui.Text(c, "Direct passwords are stored in the system credential store. An environment variable can be used instead.").FontSize(11).TextColor(theme.TextMuted)
							ui.Text(c, "SSL mode").FontSize(12).Bold()
							ui.Select(c.Key("db-profile-ssl-mode"), &d.draftSSLMode, []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"}).Width(180).Label("PostgreSQL SSL mode")
						}
					case "mysql":
						if d.draftValue != "" {
							ui.Text(c, "Legacy MySQL DSN variable").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-value"), &d.draftValue).Placeholder("e.g. MYSQL_DSN").Label("MySQL DSN environment variable").FillWidth()
							ui.Text(c, "Clear this field to use host, username, and password fields instead.").FontSize(11).TextColor(theme.TextMuted)
						} else {
							ui.Text(c, "Server").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-host"), &d.draftHost).Placeholder("db.example.com").Label("MySQL host").FillWidth()
							ui.Text(c, "Port").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-port"), &d.draftPort).Label("MySQL port").Width(120)
							ui.Text(c, "Database").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-database"), &d.draftDatabase).Label("MySQL database").FillWidth()
							ui.Text(c, "Username").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-user"), &d.draftUser).Label("MySQL username").FillWidth()
							a.databasePasswordField(c, d, "MySQL password")
							ui.Text(c, "Password environment variable (optional)").FontSize(12).Bold()
							ui.TextInput(c.Key("db-profile-password-env"), &d.draftPasswordEnv).Placeholder("e.g. DATABASE_PASSWORD").Label("MySQL password environment variable").FillWidth()
							ui.Text(c, "TLS mode").FontSize(12).Bold()
							ui.Select(c.Key("db-profile-ssl-mode"), &d.draftSSLMode, []string{"disabled", "preferred", "required", "skip-verify"}).Width(180).Label("MySQL TLS mode")
							if d.draftSSLMode == "skip-verify" {
								ui.Text(c, "Skip Verify disables certificate checks and is vulnerable to interception.").FontSize(11).TextColor(theme.Danger)
							}
						}
						ui.Text(c, "Use a SELECT-only account.").FontSize(11).TextColor(theme.TextMuted)
					case "sqlserver":
						ui.Text(c, "Server host").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-host"), &d.draftHost).Placeholder("sql.example.com").Label("SQL Server host").FillWidth()
						ui.Text(c, "Port").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-port"), &d.draftPort).Label("SQL Server port").Width(120)
						ui.Text(c, "Database").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-database"), &d.draftDatabase).Label("SQL Server database").FillWidth()
						ui.Text(c, "Username").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-user"), &d.draftUser).Label("SQL Server username").FillWidth()
						a.databasePasswordField(c, d, "SQL Server password")
						ui.Text(c, "Password environment variable (optional)").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-password-env"), &d.draftPasswordEnv).Placeholder("e.g. SQLSERVER_PASSWORD").Label("SQL Server password environment variable").FillWidth()
						ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "Trust server certificate").FontSize(12).Grow(1)
							ui.Switch(c.Key("db-profile-trust"), &d.draftTrust).Label("Trust SQL Server certificate without validation")
						})
						ui.Text(c, "Encrypted connections validate certificates by default. Direct passwords are stored in the system credential store. Use a SELECT-only account.").FontSize(11).TextColor(theme.TextMuted)
					default:
						ui.Text(c, "SQLite file").FontSize(12).Bold()
						ui.TextInput(c.Key("db-profile-value"), &d.draftValue).Placeholder("/path/to/database.sqlite").Label("Absolute SQLite file path").FillWidth()
						ui.Text(c, "Choose an existing file. Larik opens it read only.").FontSize(11).TextColor(theme.TextMuted)
					}
				})
				ui.Row(c).FillWidth().Gap(14).Padding(14, 0).AlignItems(ui.Center).BorderWidth(1, 0, 1, 0).BorderColor(theme.Border).Children(func() {
					ui.Column(c).Grow(1).Gap(3).Children(func() {
						ui.Text(c, "Agent access").FontSize(12).Bold()
						ui.Text(c, "Allow the agent to inspect this connection and send results to your model.").FontSize(11).TextColor(theme.TextMuted)
					})
					ui.Switch(c.Key("db-profile-agent"), &d.draftAgent).Label("Allow the agent to inspect this connection")
				})
				if d.status != "" {
					color := theme.Danger
					if d.status == "Connection successful" {
						color = theme.Success
					}
					ui.Text(c, d.status).FontSize(12).TextColor(color)
				}
				actions := func() {
					if d.oldName != "" && ui.Button(c.Key("db-delete-profile"), "Delete connection").TextColor(theme.Danger).Clicked() {
						d.deleting = true
					}
					ui.Spacer(c)
					if ui.Button(c.Key("db-cancel-profile"), "Cancel").Clicked() {
						d.editing, d.status, d.draftPassword = false, "", ""
					}
					if ui.Button(c.Key("db-test-profile"), map[bool]string{true: "Testing…", false: "Test Connection"}[d.testingConnection]).Clicked() && !d.testingConnection {
						a.testDatabaseConnection(tb)
					}
					if ui.Button(c.Key("db-save-profile"), "Save connection").Background(theme.Accent).TextColor(theme.AccentText).Clicked() {
						a.saveDatabaseProfile(tb)
					}
				}
				if d.narrow {
					ui.Column(c).FillWidth().Gap(8).Children(func() {
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							if d.oldName != "" && ui.Button(c.Key("db-delete-profile"), "Delete connection").TextColor(theme.Danger).Clicked() {
								d.deleting = true
							}
							ui.Spacer(c)
							if ui.Button(c.Key("db-cancel-profile"), "Cancel").Clicked() {
								d.editing, d.status, d.draftPassword = false, "", ""
							}
						})
						ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
							if ui.Button(c.Key("db-test-profile"), map[bool]string{true: "Testing…", false: "Test Connection"}[d.testingConnection]).Clicked() && !d.testingConnection {
								a.testDatabaseConnection(tb)
							}
						})
						ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
							if ui.Button(c.Key("db-save-profile"), "Save connection").Background(theme.Accent).TextColor(theme.AccentText).Clicked() {
								a.saveDatabaseProfile(tb)
							}
						})
					})
				} else {
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(actions)
				}
				if d.deleting {
					ui.Column(c).FillWidth().Padding(12).Gap(8).Border(1, theme.Danger.Alpha(0.4)).Radius(8).Background(theme.Danger.Alpha(0.06)).Children(func() {
						ui.Text(c, "Delete "+d.oldName+"?").FontSize(12).Bold()
						ui.Text(c, "This removes the saved connection. The database itself is untouched.").FontSize(11).TextColor(theme.TextMuted)
						ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
							if ui.Button(c.Key("db-cancel-delete"), "Keep connection").Clicked() {
								d.deleting = false
							}
							if ui.Button(c.Key("db-confirm-delete"), "Delete connection").Background(theme.Danger).TextColor(theme.AccentText).Clicked() {
								a.deleteDatabaseProfile(tb, d.oldName)
								d.editing = false
							}
						})
					})
				}
			})
		})
	})
}

func (a *App) databaseSidebar(c *ui.Context, theme *ui.Theme) {
	ui.SidebarSection(c.Key("desktop-databases"), "Databases", nil, func() {
		for _, name := range a.databaseStore.names() {
			p, _ := a.databaseStore.get(name)
			item := ui.SidebarItem(c, "database:"+name, iconDatabase, name).Tooltip(databaseDriverLabel(p.Driver) + " · " + name)
			item.Children(func() { ui.Text(c, databaseDriverLabel(p.Driver)).FontSize(10).TextColor(theme.TextMuted).SingleLine() })
			if item.Clicked() {
				a.openDatabaseProfile(name)
			}
			item.ContextMenu(func(m *ui.Menu) {
				if m.Item("Open").Chosen() {
					a.openDatabaseProfile(name)
				}
				if m.Item("Edit Connection").Chosen() {
					a.openDatabaseProfile(name)
					a.beginDatabaseProfile(a.focused(), name)
				}
				if m.Item("Delete Connection").Chosen() {
					a.openDatabaseProfile(name)
					a.beginDatabaseProfile(a.focused(), name)
					a.focused().database.deleting = true
				}
			})
		}
		row := ui.ButtonBase(c.Key("db-sidebar-add")).Gap(6).Padding(4, 12, 4, 14).Radius(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label("Add database connection").Children(func() {
			ui.Icon(c, iconPlus).FontSize(12).TextColor(theme.TextMuted)
			ui.Text(c, "Add connection").FontSize(12).TextColor(theme.TextMuted)
		})
		if row.Clicked() {
			a.openDatabase(a.currentDir())
			a.beginDatabaseProfile(a.focused(), "")
		}
	})
}
