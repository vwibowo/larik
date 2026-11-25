package desktop

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/egoist/mygo/ui"

	"larik/internal/database"
	"larik/internal/desktop/panes"
	browserpane "larik/internal/desktop/panes/browser"
	databasepane "larik/internal/desktop/panes/database"
	filespane "larik/internal/desktop/panes/files"
	terminalpane "larik/internal/desktop/panes/terminal"
)

func init() {
	paneVariants = makePaneVariants()

	newDatabaseState = databasepane.NewState
	newDatabaseRuntime = func(a *App, tb *tab) databaseRuntime {
		d := tb.databaseState()
		return databasepane.Runtime{
			State: d, Manager: a.databaseStore.Manager, Profile: a.databaseStore.Get,
			Work: a.databaseWork, Update: a.update,
			Alive:    func() bool { return tb.databaseState() == d },
			CopyText: a.copyText,
		}
	}
	dbBeginProfile = func(d *databaseState, name string, store databaseStore) { databasepane.BeginProfile(d, name, store) }
	dbDraftConfig = func(d *databaseState, store databaseStore) (database.ConnectionConfig, error) {
		return databasepane.DraftConfig(d, store)
	}
	dbTestConnection = func(d *databaseState, store databaseStore, work, update func(func()), alive func() bool) {
		databasepane.TestConnection(d, store, work, update, alive)
	}
	dbSaveProfile = func(d *databaseState, store databaseStore) (string, string, bool) {
		return databasepane.SaveProfile(d, store)
	}
	dbDeleteProfile = func(name string, store databaseStore) error { return databasepane.DeleteProfile(name, store) }
	dbHookDatabaseObjectMenu = func(a *App, m *ui.Menu, tb *tab, object database.Table) {
		databasepane.ObjectMenu(m, object, tb.databaseState().Driver, databasepane.ObjectMenuActions{
			Open:          func() { a.openDatabaseObject(tb, object) },
			OpenStructure: func() { a.openDatabaseStructure(tb, object) },
			CopyText:      a.copyText,
			CopyFunction:  func() { a.copyDatabaseFunctionDefinition(tb, object) },
			CopyCreation:  func() { a.copyDatabaseCreationStatement(tb, object) },
			ExportCSV:     func() { a.exportDatabaseObject(tb, object) },
		})
	}
	dbHookDatabasePasswordField = func(a *App, c *ui.Context, d *databaseState, label string) {
		p, ok := a.databaseStore.Get(d.OldName)
		databasepane.PasswordField(c, d, label, ok && p.HasPassword)
	}
	dbHookDatabaseHeader = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {
		p, _ := a.databaseStore.Get(tb.databaseState().Profile)
		databasepane.Header(c, theme, databasepane.HeaderOptions{
			State: tb.databaseState(), Driver: p.Driver, RefreshShortcut: keyText("Cmd+R"),
			DatabaseIcon: iconDatabase, ReloadIcon: iconReload, PanelIcon: iconPanel, PanelRightIcon: iconPanelRight,
			IconButton: func(id string, icon *ui.SVG, tip string, disabled bool, action func()) {
				a.iconButton(c, theme, id, icon, tip, disabled, action)
			},
			Refresh: func() { a.loadDatabaseSchema(tb) }, Add: func() { a.beginDatabaseProfile(tb, "") },
		})
	}
	dbHookDatabaseEmpty = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {
		databasepane.Empty(c, theme, databasepane.EmptyOptions{
			State: tb.databaseState(), Names: a.databaseStore.Names(), DatabaseIcon: iconDatabase,
			Refresh: func() { a.loadDatabaseSchema(tb) }, Add: func() { a.beginDatabaseProfile(tb, "") },
		})
	}
	dbHookDatabaseObjects = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {
		databasepane.Objects(c, theme, tb.databaseState(),
			func(object database.Table) { a.openDatabaseObject(tb, object) },
			func(m *ui.Menu, object database.Table) { a.databaseObjectMenu(m, tb, object) })
	}
	dbHookDatabaseCenter = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {
		a.ensureDatabaseEditor(tb)
		databasepane.Center(c, theme, databasepane.CenterOptions{
			State: tb.databaseState(), PaneOpacity: a.paneOpacity,
			BackIcon: iconBack, CopyIcon: iconCopy, RunIcon: iconRun, RunShortcut: keyText("Cmd+Enter"),
			IconButton: func(id string, icon *ui.SVG, tip string, disabled bool, action func()) {
				a.iconButton(c, theme, id, icon, tip, disabled, action)
			},
			OpenObject: func(object database.Table) { a.openDatabaseObject(tb, object) },
			RunQuery:   func() { a.runDatabaseQuery(tb) },
			RunSQL: func(query string) {
				a.runDatabaseSQL(tb, query, "SQL query", func(ctx context.Context, profile, query string) (database.Result, error) {
					return a.databaseStore.Manager().Query(ctx, profile, query, database.DefaultRows, false)
				})
			},
			RunAnalyze:  func(query string) { a.runDatabaseAnalyze(tb, query) },
			DrawResults: func() { a.databaseResults(c, theme, tb) },
		})
	}
	dbHookDatabaseResults = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {
		_, _, sharedFailure := paneFeedbackState(tb)
		databasepane.Results(c, theme, databasepane.ResultsOptions{
			State: tb.databaseState(), PaneOpacity: a.paneOpacity, HasSharedFailure: sharedFailure,
			CopyIcon: iconCopy, ExportIcon: iconExport, BackIcon: iconBack, ForwardIcon: iconForward,
			IconButton: func(id string, icon *ui.SVG, tip string, disabled bool, action func()) {
				a.iconButton(c, theme, id, icon, tip, disabled, action)
			},
			Export:   func() { a.exportDatabaseResult(tb) },
			LoadRows: func() { a.loadDatabaseRows(tb, true) },
		})
	}
	dbHookDatabaseInspector = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {
		databasepane.Inspector(c, theme, databasepane.InspectorOptions{
			State: tb.databaseState(), BackIcon: iconBack, CopyIcon: iconCopy,
			IconButton: func(id string, icon *ui.SVG, tip string, disabled bool, action func()) {
				a.iconButton(c, theme, id, icon, tip, disabled, action)
			},
		})
	}
	dbHookDatabaseProfileForm = func(a *App, c *ui.Context, theme *ui.Theme, tb *tab) {
		databasepane.ProfileForm(c, theme, databasepane.FormOptions{
			State:         tb.databaseState(),
			PasswordField: func(label string) { a.databasePasswordField(c, tb.databaseState(), label) },
			Test:          func() { a.testDatabaseConnection(tb) },
			Save:          func() { a.saveDatabaseProfile(tb) },
			Delete:        func(name string) { a.deleteDatabaseProfile(tb, name) },
		})
	}
	dbHookDatabaseSidebar = func(a *App, c *ui.Context, theme *ui.Theme) {
		databasepane.Sidebar(c, theme, databasepane.SidebarOptions{
			Names:        a.databaseStore.Names(),
			Driver:       func(name string) string { p, _ := a.databaseStore.Get(name); return p.Driver },
			DatabaseIcon: iconDatabase, PlusIcon: iconPlus,
			Open: a.openDatabaseProfile,
			Edit: func(name string) { a.openDatabaseProfile(name); a.beginDatabaseProfile(a.focused(), name) },
			Delete: func(name string) {
				a.openDatabaseProfile(name)
				a.beginDatabaseProfile(a.focused(), name)
				a.focused().databaseState().Deleting = true
			},
			Add: func() { a.openDatabase(a.currentDir()); a.beginDatabaseProfile(a.focused(), "") },
		})
	}

	paneRegistry = paneRegistryFor(paneVariants)
	databaseCompletion = databasepane.Completions
	databaseIdentifier = func(name string) string { return databasepane.SQLIdentifierFor("", name) }
	databaseEncodeCSV = databasepane.CSV
	databaseEncodeTSV = databasepane.TSV
	databaseExecute = databasepane.Execute
	databaseRender = databasepane.View
	loadDatabaseStore = func() databaseStore { return databasepane.LoadStore() }
	loadDatabaseStoreWithSecrets = func(secrets databaseSecrets) databaseStore { return databasepane.LoadStoreWithSecrets(secrets) }
	databaseSecretKey = databasepane.SecretKey
}

// paneVariant binds a domain to the window. The domain's stable identity and
// presentation order live here; the window keeps ownership of tab placement.
type paneVariant struct {
	panes.Descriptor
	menuTitle  string
	menuKey    string
	menuOpen   func(*App)
	key        ui.Key
	icon       *ui.SVG
	start      func(*App, string) *tab
	render     func(*App, *ui.Context, *ui.Theme, *tab) ui.Element
	sideRender func(*App, *ui.Context, *ui.Theme)
	snapshot   func(*tab) json.RawMessage
	restore    func(*App, panes.Snapshot, agentResume) *tab
	sidebar    func(*App, *ui.Context, *ui.Theme)
	selected   func(*tab) string
	openSide   func(*App, string) bool
	control    func(*App, Request) Response
	env        func(*App) []string
}

var paneVariants []paneVariant

func makePaneVariants() []paneVariant {
	return []paneVariant{
		{Descriptor: panes.Descriptor{Kind: panes.Session, Title: "Larik Session", About: "Ask larik to work on this folder", Key: "L"}, menuTitle: "New Larik Session", menuOpen: func(a *App) { a.newTab(a.currentDir(), "", "") }, key: ui.KeyL, icon: iconChat,
			start: func(a *App, dir string) *tab { return a.startTab(dir, "", "") }, render: renderTerminalPane},
		{Descriptor: panes.Descriptor{Kind: panes.Worktree, Title: "Session in a Worktree", About: "A session on a branch of its own, apart from your checkout", Key: "W"}, menuTitle: "New Session in Worktree", menuKey: "Alt+Cmd+T", menuOpen: func(a *App) { a.newWorktreeSession(a.currentDir()) }, key: ui.KeyW, icon: iconBranch},
		{Descriptor: panes.Descriptor{Kind: panes.Terminal, Title: "Terminal", About: "Your shell, or a dev server, beside the sessions", Key: "T"}, menuTitle: "New Terminal", menuKey: "Shift+Cmd+N", menuOpen: func(a *App) { a.openShell() }, key: ui.KeyT, icon: iconTerm,
			start: func(a *App, dir string) *tab { return a.startShell(dir, "") }, render: renderTerminalPane, snapshot: snapshotTerminal, restore: restoreTerminal},
		{Descriptor: panes.Descriptor{Kind: panes.Browser, Title: "Browser", About: "A web page, such as what the dev server serves", Key: "B"}, menuTitle: "New Browser Pane", menuKey: "Alt+Cmd+N", menuOpen: func(a *App) { a.openBrowser("") }, key: ui.KeyB, icon: iconGlobe,
			start: func(a *App, dir string) *tab {
				t := a.startBrowser("")
				if t != nil {
					t.dir = dir
				}
				return t
			}, render: renderBrowserPane, snapshot: snapshotBrowser, restore: restoreBrowser,
			control: func(a *App, req Request) Response { return a.pageControl(req) }},
		{Descriptor: panes.Descriptor{Kind: panes.Files, Title: "Files", About: "Browse this folder and preview files", Key: "F"}, menuTitle: "New Files Pane", menuOpen: func(a *App) { a.openFiles(a.currentDir()) }, key: ui.KeyF, icon: iconFolder,
			start: func(a *App, dir string) *tab { return a.startFiles(dir) }, render: func(a *App, c *ui.Context, theme *ui.Theme, t *tab) ui.Element { return a.filesPane(c, theme, t) }, snapshot: snapshotFiles, restore: restoreFiles},
		{Descriptor: panes.Descriptor{Kind: panes.Database, Title: "Database", About: "Browse databases and run read-only SQL", Key: "D"}, menuTitle: "New Database Pane", menuOpen: func(a *App) { a.openDatabase(a.currentDir()) }, key: ui.KeyD, icon: iconDatabase,
			start: func(a *App, dir string) *tab { return a.startDatabase(dir) }, render: func(a *App, c *ui.Context, theme *ui.Theme, t *tab) ui.Element { return a.databasePane(c, theme, t) },
			snapshot: func(t *tab) json.RawMessage {
				return paneData(databasepane.Snapshot{View: paneData(saveDatabaseView(t.databaseState()))})
			},
			restore: func(a *App, s panes.Snapshot, _ agentResume) *tab {
				var v databasepane.Snapshot
				if json.Unmarshal(s.Data, &v) != nil {
					return nil
				}
				var saved savedDatabaseView
				if json.Unmarshal(v.View, &saved) != nil {
					return nil
				}
				t := a.startDatabase(s.Dir)
				if t == nil {
					return nil
				}
				restoreDatabaseView(t.databaseState(), saved)
				if t.databaseState().Profile != "" {
					a.loadDatabaseSchema(t)
				}
				return t
			},
			sidebar: func(a *App, c *ui.Context, theme *ui.Theme) { a.databaseSidebar(c, theme) },
			selected: func(t *tab) string {
				if t.databaseState() != nil && t.databaseState().Profile != "" {
					return "database:" + t.databaseState().Profile
				}
				return ""
			},
			control: func(a *App, req Request) Response { return a.databaseControl(req) },
			env: func(a *App) []string {
				if a.databaseStore != nil {
					return a.databaseStore.MaskedEnv()
				}
				return nil
			},
			openSide: func(a *App, key string) bool {
				if name, ok := strings.CutPrefix(key, "database:"); ok {
					a.openDatabaseProfile(name)
					return true
				}
				return false
			}},
	}
}

func paneRegistryFor(variants []paneVariant) *panes.Registry {
	r := new(panes.Registry)
	for _, v := range variants {
		r.Register(v.Descriptor)
	}
	r.Register(panes.Descriptor{Kind: panes.Chooser, Title: "New Pane"})
	r.Register(panes.Descriptor{Kind: panes.Changes, Title: "Changes"})
	return r
}

var paneRegistry *panes.Registry

func paneVariantFor(kind panes.Kind) (paneVariant, bool) {
	if paneRegistry == nil {
		return paneVariant{}, false
	}
	descriptor, enabled := paneRegistry.Get(kind)
	if !enabled {
		return paneVariant{}, false
	}
	if kind == panes.Chooser {
		return paneVariant{Descriptor: descriptor, icon: iconPlus,
			render: func(a *App, c *ui.Context, theme *ui.Theme, t *tab) ui.Element {
				return a.chooserPane(c, theme, t)
			}}, true
	}
	if kind == panes.Changes {
		return paneVariant{Descriptor: descriptor, sideRender: func(a *App, c *ui.Context, theme *ui.Theme) {
			if a.changes != nil {
				a.changesPane(c, theme, a.changes)
			}
		}}, true
	}
	for _, v := range paneVariants {
		if v.Kind == kind {
			return v, true
		}
	}
	return paneVariant{}, false
}

func (a *App) openPaneSidebarSelection(key string) bool {
	for _, variant := range paneVariants {
		if variant.openSide != nil && variant.openSide(a, key) {
			return true
		}
	}
	return false
}

func (a *App) paneControl(kind panes.Kind, req Request) Response {
	if variant, ok := paneVariantFor(kind); ok && variant.control != nil {
		return variant.control(a, req)
	}
	label := string(kind)
	if label != "" {
		label = strings.ToUpper(label[:1]) + label[1:]
	}
	return Response{Error: label + " panes are unavailable"}
}

func (a *App) tabPaneKind(t *tab) panes.Kind {
	switch {
	case t == nil:
		return ""
	case t.pane != nil:
		return t.pane.Kind()
	case t.isBrowser():
		return panes.Browser
	case t.shell():
		return panes.Terminal
	default:
		return panes.Session
	}
}

func renderTerminalPane(a *App, c *ui.Context, theme *ui.Theme, tb *tab) ui.Element {
	var find func()
	if tb.finding {
		find = func() { a.findBar(c, theme, tb) }
	}
	return terminalpane.View(c, tb.id, tb.terminal(), find)
}

func renderBrowserPane(a *App, c *ui.Context, theme *ui.Theme, tb *tab) ui.Element {
	var find func()
	if tb.finding {
		find = func() { a.pageFindBar(c, theme, tb) }
	}
	return browserpane.View(c, tb.id, tb.browserState().Web,
		func() { a.browserTabs(c, theme, tb) },
		func() { a.addressBar(c, theme, tb) },
		func() { a.siteBars(c, theme, tb) }, find)
}

func snapshotTerminal(t *tab) json.RawMessage {
	return paneData(terminalpane.Snapshot{Dir: t.dir, Run: t.run})
}

func restoreTerminal(a *App, s panes.Snapshot, agent agentResume) *tab {
	t := a.startShell(s.Dir, "") // never rerun a saved program
	if t != nil && agent.valid() {
		agent.Tab = t.id
		if mapped := agentForTab(t, agent); mapped.valid() {
			t.agent, t.agentOffered = mapped, true
		}
	}
	return t
}

func snapshotBrowser(t *tab) json.RawMessage {
	v := browserpane.Snapshot{URL: t.browserState().URL, RemotePath: t.remoteBookmarkSuffix}
	if t.remotePort.port != 0 {
		v.RemotePort = browserpane.RemotePort{Name: t.remotePort.name, Port: t.remotePort.port}
		v.URL = blankPage
	}
	return paneData(v)
}

func restoreBrowser(a *App, s panes.Snapshot, _ agentResume) *tab {
	var v browserpane.Snapshot
	if json.Unmarshal(s.Data, &v) != nil {
		return nil
	}
	t := a.startBrowser(v.URL)
	if t == nil {
		return nil
	}
	t.browserState().FocusAddress = false
	if s.Dir != "" {
		t.dir = s.Dir
	}
	if p := v.RemotePort; p.Name != "" && p.Port > 0 && p.Port <= 65535 {
		if !isRemote(t.dir) {
			t.dir = remotePath(p.Name, "")
		}
		t.remotePort, t.remotePortLost = remotePortKey{p.Name, p.Port}, true
		t.remoteBookmarkSuffix = v.RemotePath
	}
	return t
}

func snapshotFiles(t *tab) json.RawMessage {
	return paneData(filespane.Snapshot{Pinned: t.filesState().PinnedRoot})
}

func restoreFiles(a *App, s panes.Snapshot, _ agentResume) *tab {
	var v filespane.Snapshot
	if json.Unmarshal(s.Data, &v) != nil {
		return nil
	}
	t := a.startFiles(s.Dir)
	if t != nil {
		t.filesState().PinnedRoot = v.Pinned
	}
	return t
}
