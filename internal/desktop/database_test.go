package desktop

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"larik/internal/config"
	"larik/internal/database"
)

func databaseFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(project, "sample.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT); INSERT INTO notes(body) VALUES ('hello'),('world')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return project, path
}

type fakeDatabaseSecrets map[string][]byte

func (s fakeDatabaseSecrets) Set(key string, value []byte) error {
	s[key] = append([]byte(nil), value...)
	return nil
}
func (s fakeDatabaseSecrets) Get(key string) ([]byte, error) {
	value, ok := s[key]
	if !ok {
		return nil, mygo.ErrSecretNotFound
	}
	return append([]byte(nil), value...), nil
}
func (s fakeDatabaseSecrets) Delete(key string) error {
	if _, ok := s[key]; !ok {
		return mygo.ErrSecretNotFound
	}
	delete(s, key)
	return nil
}

func TestRefreshFocusedDatabasePaneSkipsWhileLoading(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	tb := &tab{id: "db", dir: "/work", database: &databaseState{loading: true}}
	a.tabs = []*tab{tb}
	a.activate(tb)
	a.refreshFocused()
	if !tb.database.loading {
		t.Fatal("refresh should not start another schema load while one is active")
	}
}

func TestDatabaseCellTooltipVisible(t *testing.T) {
	project, path := databaseFixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	value := "2026-09-24T17:28:01+00:00"
	if _, err := db.Exec("UPDATE notes SET body = ? WHERE id = 1", value); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	a.paneOpacity = 0.45
	tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(ui.DarkTheme()); a.view(c) }, 1400, 800)
	tt.Frame()
	cell, ok := tt.Find(value)
	if !ok {
		t.Fatalf("cell missing: %q", tt.Texts())
	}
	tt.Move(cell.X+cell.W/2, cell.Y+cell.H/2)
	tt.Advance(time.Second)
	snapshot(t, tt, "database-cell-tooltip.png")
	if n := settingsTextCount(tt.Texts(), value); n < 2 {
		t.Fatalf("tooltip missing: %q", tt.Texts())
	}
	darkPixels := 0
	for y := int(cell.Y) - 18; y < int(cell.Y)-7; y++ {
		for x := int(cell.X) + 15; x < int(cell.X+cell.W)-15; x++ {
			p := tt.Image().RGBAAt(x, y)
			if p.R < 100 && p.G < 100 && p.B < 100 {
				darkPixels++
			}
		}
	}
	if darkPixels < 10 {
		t.Fatal("tooltip has no visible dark text on its light fill")
	}
}

func TestDatabaseBrowserManyTables(t *testing.T) {
	project, path := databaseFixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 36 {
		if _, err := db.Exec(fmt.Sprintf("CREATE TABLE table_%02d (id INTEGER)", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(ui.DarkTheme()); a.view(c) }, 1400, 800)
	tt.Frame()
	snapshot(t, tt, "database-tree-dark.png")
	if !tt.HasText("table_00") || !tt.HasText("main") || tt.HasText("37 tables and views") {
		t.Fatalf("dense table browser: %q", tt.Texts())
	}
}

func TestDatabaseConnectionFormLayout(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	a.openDatabase(project)
	d := a.focused().database
	a.beginDatabaseProfile(a.focused(), "")
	wide := ui.NewTester(func(c *ui.Context) { c.SetTheme(ui.DarkTheme()); a.view(c) }, 1400, 800)
	wide.Frame()
	snapshot(t, wide, "database-add-connection-dark.png")
	if !wide.HasText("Name") || !wide.HasText("Database type") || !wide.HasText("SQLite file") || !wide.HasText("Agent access") {
		t.Fatalf("connection labels missing: %q", wide.Texts())
	}
	if field, ok := wide.Find("Connection name"); !ok || field.W > 600 || field.W < 300 {
		t.Fatalf("connection form not bounded: %+v, found=%v", field, ok)
	}
	if n := settingsTextCount(wide.Texts(), "Add connection"); n != 2 {
		t.Fatalf("connection form has a duplicate header action: %d, %q", n, wide.Texts())
	}
	if err := wide.Click("PostgreSQL"); err != nil {
		t.Fatal(err)
	}
	wide.Frame()
	if d.draftDriver != "postgres" || !wide.HasText("Host") || !wide.HasText("Database name") || !wide.HasText("Username") || !wide.HasText("PostgreSQL password") || !wide.HasText("PostgreSQL SSL mode") || !wide.HasText("Password environment variable") {
		t.Fatalf("PostgreSQL form: driver %q, texts %q", d.draftDriver, wide.Texts())
	}
	snapshot(t, wide, "database-add-postgres-dark.png")
	if err := wide.Click("SQLite"); err != nil {
		t.Fatal(err)
	}
	d.draftName, d.draftValue = "local", path
	narrow := ui.NewTester(a.view, 560, 500)
	narrow.Frame()
	snapshot(t, narrow, "database-add-connection-narrow.png")
	if field, ok := narrow.Find("Connection name"); !ok || field.W > 500 {
		t.Fatalf("narrow connection form overflows: %+v, found=%v", field, ok)
	}
	narrow.Scroll(420, 400, 0, 500)
	narrow.Frame()
	snapshot(t, narrow, "database-add-connection-narrow-actions.png")
	if err := narrow.Click("Save connection"); err != nil {
		t.Fatal(err)
	}
	narrow.Frame()
	if d.editing || d.profile != "local" {
		button, _ := narrow.Find("Save connection")
		t.Fatalf("form did not save the connection: editing=%v profile=%q status=%q driver=%q path=%q button=%+v", d.editing, d.profile, d.status, d.draftDriver, d.draftValue, button)
	}
}

func TestDatabaseNewDriverFormsAndStorage(t *testing.T) {
	project, _ := databaseFixture(t)
	t.Setenv("LARIK_TEST_SQL_PASSWORD", "very_secret")
	t.Setenv("LARIK_TEST_MYSQL_DSN", "reader:very_secret@tcp(localhost:3306)/app")
	a := New(Options{Cwd: project})
	defer a.Close()
	a.openDatabase(project)
	a.beginDatabaseProfile(a.focused(), "")
	tt := ui.NewTester(a.view, 850, 650)
	tt.Frame()
	if err := tt.Click("SQL Server"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, label := range []string{"SQL Server host", "SQL Server port", "SQL Server database", "SQL Server username", "SQL Server password", "SQL Server password environment variable", "Trust SQL Server certificate without validation"} {
		if !tt.HasText(label) {
			t.Fatalf("missing SQL Server field %q", label)
		}
	}
	if a.focused().database.draftTrust {
		t.Fatal("certificate trust started enabled")
	}
	snapshot(t, tt, "database-add-sqlserver.png")
	if err := tt.Click("MySQL"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, label := range []string{"MySQL host", "MySQL port", "MySQL database", "MySQL username", "MySQL password", "MySQL TLS mode"} {
		if !tt.HasText(label) {
			t.Fatalf("MySQL connection form is missing %q: %q", label, tt.Texts())
		}
	}
	profiles := map[string]database.ConnectionConfig{
		"sql": {Driver: "sqlserver", Host: "localhost", Port: 1433, Database: "app", User: "reader", PasswordEnv: "LARIK_TEST_SQL_PASSWORD"},
		"my":  {Driver: "mysql", DSNEnv: "LARIK_TEST_MYSQL_DSN"},
	}
	if err := a.databaseStore.save(profiles); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(a.databaseStore.path)
	if err != nil || strings.Contains(string(data), "very_secret") {
		t.Fatalf("credentials stored: %v", err)
	}
	masked := a.databaseStore.maskedEnv()
	if !slices.Contains(masked, "LARIK_TEST_SQL_PASSWORD=") || !slices.Contains(masked, "LARIK_TEST_MYSQL_DSN=") {
		t.Fatalf("unmasked variables: %v", masked)
	}
	other := New(Options{Cwd: project})
	defer other.Close()
	if p, ok := other.databaseStore.get("sql"); !ok || p.TrustServerCertificate || p.PasswordEnv != "LARIK_TEST_SQL_PASSWORD" {
		t.Fatalf("restored SQL Server: %+v %v", p, ok)
	}
	profiles["sql"] = database.ConnectionConfig{Driver: "sqlserver", Host: "localhost", Port: 1433, Database: "app", User: "reader", PasswordEnv: "LARIK_TEST_SQL_PASSWORD", TrustServerCertificate: true}
	delete(profiles, "my")
	if err := a.databaseStore.save(profiles); err != nil {
		t.Fatal(err)
	}
	if p, ok := a.databaseStore.get("sql"); !ok || !p.TrustServerCertificate {
		t.Fatalf("edited SQL Server: %+v %v", p, ok)
	}
	if _, ok := a.databaseStore.get("my"); ok {
		t.Fatal("MySQL connection was not deleted")
	}
}

func TestDatabasePasswordUsesSystemCredentialStore(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	secrets := fakeDatabaseSecrets{}
	store := &databaseStore{path: config.DesktopDatabasePath(), profiles: map[string]database.ConnectionConfig{}, secrets: secrets}
	const password = "very_secret_password"
	profile := database.ConnectionConfig{Driver: "postgres", Host: "db.example.com", Port: 5432, Database: "app", User: "reader", Password: password, SSLMode: "verify-full"}
	if err := store.save(map[string]database.ConnectionConfig{"analytics": profile}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), password) || !strings.Contains(string(data), `"has_password": true`) {
		t.Fatalf("settings did not keep the password in the credential store: %s", data)
	}
	if string(secrets[databaseSecretKey("analytics")]) != password {
		t.Fatal("password missing from system credential store")
	}

	reloaded := loadDatabaseStoreWithSecrets(secrets)
	if got, ok := reloaded.get("analytics"); !ok || got.Password != password || got.SSLMode != "verify-full" {
		t.Fatalf("saved connection was not restored: %+v %v", got, ok)
	}
	if err := reloaded.save(map[string]database.ConnectionConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := secrets[databaseSecretKey("analytics")]; ok {
		t.Fatal("deleted connection left a password in the credential store")
	}
}

func TestDatabasePasswordStoreFailureDoesNotPersistProfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	secrets := failingDatabaseSecrets{}
	store := &databaseStore{path: config.DesktopDatabasePath(), profiles: map[string]database.ConnectionConfig{}, secrets: secrets}
	err := store.save(map[string]database.ConnectionConfig{"analytics": {Driver: "postgres", Host: "db.example.com", Port: 5432, Database: "app", User: "reader", Password: "secret"}})
	if err == nil || !strings.Contains(err.Error(), "system credential store") {
		t.Fatalf("expected secure storage failure, got %v", err)
	}
	if _, ok := store.get("analytics"); ok {
		t.Fatal("profile was published without storing its password")
	}
}

type failingDatabaseSecrets struct{}

func (failingDatabaseSecrets) Set(string, []byte) error { return errors.New("keyring unavailable") }
func (failingDatabaseSecrets) Get(string) ([]byte, error) {
	return nil, errors.New("keyring unavailable")
}
func (failingDatabaseSecrets) Delete(string) error { return nil }

func TestDatabaseNewDriversAgentOptIn(t *testing.T) {
	project, _ := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	profiles := map[string]database.ConnectionConfig{
		"sql": {Driver: "sqlserver", Host: "localhost", Port: 1433, Database: "app", User: "reader", PasswordEnv: "LARIK_MISSING_SQL_PASSWORD"},
		"my":  {Driver: "mysql", DSNEnv: "LARIK_MISSING_MYSQL_DSN"},
	}
	if err := a.databaseStore.save(profiles); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("sql")
	if got := a.databaseControl(Request{Cmd: "database", Op: "list"}); got.Database == nil || len(got.Database.Panes) != 0 {
		t.Fatalf("private SQL Server pane listed: %+v", got)
	}
	profiles["sql"] = database.ConnectionConfig{Driver: "sqlserver", Host: "localhost", Port: 1433, Database: "app", User: "reader", PasswordEnv: "LARIK_MISSING_SQL_PASSWORD", AgentAccess: true}
	if err := a.databaseStore.save(profiles); err != nil {
		t.Fatal(err)
	}
	got := a.databaseControl(Request{Cmd: "database", Op: "list"})
	if got.Database == nil || len(got.Database.Panes) != 1 || got.Database.Panes[0].Driver != "sqlserver" {
		t.Fatalf("agent SQL Server pane: %+v", got)
	}
	if result := a.databaseControl(Request{Cmd: "database", Op: "query", SQL: "SELECT 1"}); result.Error == "" {
		t.Fatalf("query unexpectedly succeeded: %+v", result)
	}
	a.openDatabaseProfile("my")
	if got := a.databaseControl(Request{Cmd: "database", Op: "list"}); got.Database == nil || len(got.Database.Panes) != 1 {
		t.Fatalf("private MySQL pane listed: %+v", got)
	}
	profiles["my"] = database.ConnectionConfig{Driver: "mysql", DSNEnv: "LARIK_MISSING_MYSQL_DSN", AgentAccess: true}
	if err := a.databaseStore.save(profiles); err != nil {
		t.Fatal(err)
	}
	if got := a.databaseControl(Request{Cmd: "database", Op: "list"}); got.Database == nil || len(got.Database.Panes) != 2 {
		t.Fatalf("agent MySQL pane: %+v", got)
	}
}

func TestDatabaseSidebarAndPane(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project, DataDir: t.TempDir()})
	defer a.Close()
	a.openDatabase(project)
	tb := a.focused()
	tt := ui.NewTester(a.view, 1400, 800)
	tt.Frame()
	if !tt.HasText("Databases") || !tt.HasText("Add connection") {
		t.Fatalf("empty sidebar: %q", tt.Texts())
	}
	a.beginDatabaseProfile(tb, "")
	tb.database.draftName, tb.database.draftValue = "local", path
	a.saveDatabaseProfile(tb)
	if tb.database.editing || tb.database.status != "" {
		t.Fatalf("saved profile: %+v", tb.database)
	}
	tt.Frame()
	snapshot(t, tt, "database-wide.png")
	browserOpaque, editorOpaque, resultsOpaque, toolbarOpaque := tt.Image().RGBAAt(400, 650), tt.Image().RGBAAt(900, 240), tt.Image().RGBAAt(900, 650), tt.Image().RGBAAt(900, 20)
	a.paneOpacity = 0.45
	tt.Frame()
	snapshot(t, tt, "database-opacity.png")
	if browser, editor, results := tt.Image().RGBAAt(400, 650), tt.Image().RGBAAt(900, 240), tt.Image().RGBAAt(900, 650); browser == browserOpaque || editor == editorOpaque || results == resultsOpaque {
		t.Fatalf("Database background did not change: browser %v→%v, editor %v→%v, results %v→%v", browserOpaque, browser, editorOpaque, editor, resultsOpaque, results)
	}
	if tt.Image().RGBAAt(900, 20) != toolbarOpaque {
		t.Fatal("Database opacity changed the toolbar")
	}
	a.paneOpacity = 1
	tt.Frame()
	queryHeader, _ := tt.Find("SQL query")
	dataTab, _ := tt.Find("Data")
	if dataTab.Y-queryHeader.Y < 140 || dataTab.Y-queryHeader.Y > 240 {
		t.Fatalf("editor is not compact: SQL y=%v, results y=%v", queryHeader.Y, dataTab.Y)
	}
	for _, want := range []string{"local", "SQL query", "main.notes", "hello", "Find database objects", "Refresh database objects"} {
		if !tt.HasText(want) {
			t.Fatalf("missing %q in %q", want, tt.Texts())
		}
	}
	if tt.HasText("Objects") {
		t.Fatal("database object sidebar still has a title")
	}
	if _, ok := tt.Find("Select a row to show details on the right"); !ok {
		t.Fatal("right panel icon should remain visible before a row is selected")
	}
	if err := tt.Click("Hide tables on the left"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tb.database.showBrowser {
		t.Fatal("left panel icon did not hide the table browser")
	}
	if err := tt.Click("Show tables on the left"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tb.database.showBrowser {
		t.Fatal("left panel icon did not restore the table browser")
	}
	if err := tt.Click("main"); err != nil {
		t.Fatal(err)
	}
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	if open := tb.database.schemaOpen["main"]; open == nil || *open {
		t.Fatal("schema tree did not collapse with the Files panel keyboard behavior")
	}
	if err := tt.Click("Find database objects"); err != nil {
		t.Fatal(err)
	}
	tt.Type("notes")
	tt.Frame()
	if open := tb.database.schemaOpen["main"]; open == nil || !*open {
		t.Fatal("search did not reveal matching tables in a collapsed schema")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if p, _ := a.databaseStore.get("local"); p.AgentAccess {
		t.Fatal("new connection shared with agent")
	}
	tb.database.sql.Buffer.Set("SELECT count(*) AS total FROM notes")
	if err := tt.Click("Read-only SQL query"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyEnter)
	tt.Frame()
	if !tt.HasText("SQL query") || !tt.HasText("total") || tb.database.activeResult != databaseQuery {
		t.Fatalf("query result: %q", tt.Texts())
	}
	if err := tt.Click("2"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tb.database.currentResult().selectedRow != 0 || tb.database.showDetails {
		t.Fatalf("single click should only select a row: row %d, details %t", tb.database.currentResult().selectedRow, tb.database.showDetails)
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !tt.HasText("Details") || !tt.HasText("total") {
		t.Fatalf("row inspector: %q", tt.Texts())
	}
	if !tb.database.showDetails {
		t.Fatal("Enter did not open the inspector")
	}
	if err := tt.Click("Hide details on the right"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tb.database.showDetails {
		t.Fatal("right panel icon did not hide the inspector")
	}
	if err := tt.Click("Show details on the right"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tb.database.showDetails {
		t.Fatal("right panel icon did not restore the inspector")
	}
	snapshot(t, tt, "database-inspector.png")
	if err := tt.Click("Data"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tb.database.activeResult != databaseData || !tt.HasText("hello") || len(tb.database.results[databaseQuery].result.Rows) != 1 {
		t.Fatalf("Data/Query result isolation: %q", tt.Texts())
	}
	if err := tt.Click("Find database objects"); err != nil {
		t.Fatal(err)
	}
	tt.Type("missing")
	tt.Frame()
	if !tt.HasText("No objects match this search.") || !tt.HasText("0 of 1 objects") {
		t.Fatalf("table search: %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if !tt.HasText("notes") {
		t.Fatalf("search clear: %q", tt.Texts())
	}
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path, AgentAccess: true}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(config.DesktopDatabasePath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private store: %v %v", info, err)
	}
	data, err := os.ReadFile(config.DesktopDatabasePath())
	if err != nil || !strings.Contains(string(data), "sample.sqlite") {
		t.Fatalf("saved connection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".larik", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("project settings written: %v", err)
	}
	tb.database.activeResult = databaseQuery
	st := a.state()
	if st.DatabaseViews[0].Profile != "local" {
		t.Fatalf("saved view: %+v", st.DatabaseViews)
	}
	if st.DatabaseViews[0].ActiveResult != databaseQuery || st.DatabaseViews[0].EditorHeightPx != 180 {
		t.Fatalf("saved result tab and editor height: %+v", st.DatabaseViews[0])
	}
	saved, _ := json.Marshal(st)
	if err := os.MkdirAll(filepath.Dir(a.statePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.statePath(), saved, 0o600); err != nil {
		t.Fatal(err)
	}
	b := New(Options{Cwd: project, DataDir: a.opts.DataDir})
	defer b.Close()
	if !b.restore() || len(b.tabs) != 1 || b.tabs[0].database.profile != "local" || b.tabs[0].database.activeResult != databaseQuery {
		t.Fatalf("restored pane: %+v", b.tabs)
	}
}

func TestDatabaseNarrowAndBridge(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	tb := a.focused()
	if got := a.databaseControl(Request{Cmd: "database", Op: "list"}); got.Database == nil || len(got.Database.Panes) != 0 {
		t.Fatalf("opt-in: %+v", got)
	}
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path, AgentAccess: true}}); err != nil {
		t.Fatal(err)
	}
	if got := a.databaseControl(Request{Cmd: "database", Op: "list"}); got.Database == nil || len(got.Database.Panes) != 1 {
		t.Fatalf("list: %+v", got)
	}
	tb.database.sql.Buffer.Set("SELECT 'draft'")
	a.runDatabaseQuery(tb)
	queryRows := tb.database.results[databaseQuery].result.Rows
	res := a.databaseControl(Request{Cmd: "database", Op: "query", SQL: "SELECT body FROM notes ORDER BY id"})
	if res.Error != "" || res.Database == nil || len(res.Database.Result.Rows) != 2 || tb.database.sql.Buffer.String() != "SELECT 'draft'" || tb.database.activeResult != databaseAgent || len(tb.database.results[databaseQuery].result.Rows) != len(queryRows) {
		t.Fatalf("agent query: %+v", res)
	}
	if len(tb.database.results[databaseData].result.Rows) != 2 || tb.database.results[databaseAgent].source != "SQL query" {
		t.Fatal("agent result replaced table data or lost its source")
	}
	rows := a.databaseControl(Request{Cmd: "database", Op: "rows", Schema: "main", Table: "notes", Limit: 1})
	if rows.Error != "" || tb.database.results[databaseAgent].source != "main.notes" || len(tb.database.results[databaseData].result.Rows) != 2 || len(tb.database.results[databaseQuery].result.Rows) != len(queryRows) {
		t.Fatalf("agent rows disturbed user results: %+v", rows)
	}
	if got := a.databaseControl(Request{Cmd: "database", Op: "query", SQL: "DELETE FROM notes"}); got.Error == "" {
		t.Fatal("write accepted")
	}
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	if got := a.databaseControl(Request{Cmd: "database", Op: "query", Tab: tb.id, SQL: "SELECT 1"}); got.Error == "" {
		t.Fatal("revoked access accepted")
	}
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path, AgentAccess: true}}); err != nil {
		t.Fatal(err)
	}
	second := a.startDatabase(project)
	second.database.profile = "local"
	a.activate(second)
	if got := a.databaseControl(Request{Cmd: "database", Op: "schema"}); got.Error == "" {
		t.Fatal("ambiguous pane accepted")
	}
	if got := a.databaseControl(Request{Cmd: "database", Op: "schema", Tab: tb.id}); got.Error != "" {
		t.Fatalf("targeted pane: %+v", got)
	}
	a.closeTab(second)
	tt := ui.NewTester(a.view, 550, 650)
	tt.Frame()
	snapshot(t, tt, "database-narrow.png")
	if !tt.HasText("SQL query") || !tt.HasText("Agent") {
		t.Fatalf("narrow results: %q", tt.Texts())
	}
	tb.database.region = databaseTablesRegion
	tt.Frame()
	if !tt.HasText("Tables") || tt.HasText("SQL Query") {
		t.Fatalf("narrow browser: %q", tt.Texts())
	}
	a.closeTab(tb)
	if got := a.databaseControl(Request{Cmd: "database", Op: "schema", Tab: tb.id}); got.Error == "" {
		t.Fatal("closed pane accepted")
	}
}

func TestDatabaseResponsiveRegionsAndRestore(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	d := a.focused().database
	medium := ui.NewTester(a.view, 900, 700)
	medium.Frame()
	snapshot(t, medium, "database-medium.png")
	if !medium.HasText("Tables") || !medium.HasText("Results") || !medium.HasText("SQL query") {
		t.Fatalf("medium navigation: %q", medium.Texts())
	}
	if err := medium.Click("hello"); err != nil {
		t.Fatal(err)
	}
	medium.Frame()
	if d.region != databaseResultsRegion || !medium.HasText("hello") {
		t.Fatalf("single click should stay in results: %q", medium.Texts())
	}
	if err := medium.Click("hello"); err != nil {
		t.Fatal(err)
	}
	medium.Frame()
	if d.region != databaseDetailsRegion || !medium.HasText("Back to results") || !medium.HasText("body") {
		t.Fatalf("double click row details navigation: %q", medium.Texts())
	}
	if err := medium.Click("Back to results"); err != nil {
		t.Fatal(err)
	}
	medium.Frame()
	if d.region != databaseResultsRegion || !medium.HasText("hello") {
		t.Fatalf("back to data: %q", medium.Texts())
	}
	d.activeResult, d.editorHeight = databaseQuery, 225
	saved := saveDatabaseView(d)
	if saved.ActiveResult != databaseQuery || saved.EditorHeightPx != 225 || saved.EditorHeight != 0 || saved.PageSize != database.DefaultRows {
		t.Fatalf("saved database layout: %+v", saved)
	}
	b := &databaseState{editorHeight: 180, region: databaseResultsRegion}
	restoreDatabaseView(b, saved)
	if b.activeResult != databaseQuery || b.editorHeight != 225 || b.pageSize != database.DefaultRows {
		t.Fatalf("restored database layout: %+v", b)
	}
	legacy := &databaseState{editorHeight: 180, region: databaseResultsRegion}
	restoreDatabaseView(legacy, savedDatabaseView{Profile: "local", EditorHeight: 34, ShowBrowser: true, ShowDetails: true})
	a.focused().database = legacy
	wide := ui.NewTester(a.view, 1400, 800)
	wide.Frame()
	if legacy.editorHeight != 180 || legacy.legacyEditorPercent != 0 {
		t.Fatalf("legacy editor height: %v, %v", legacy.editorHeight, legacy.legacyEditorPercent)
	}
	custom := &databaseState{editorHeight: 180, region: databaseResultsRegion}
	restoreDatabaseView(custom, savedDatabaseView{Profile: "local", EditorHeight: 50, ShowBrowser: true, ShowDetails: true})
	a.focused().database = custom
	wide.Frame()
	if custom.editorHeight <= 180 || custom.editorHeight > 320 || custom.legacyEditorPercent != 0 {
		t.Fatalf("legacy custom editor height: %v, %v", custom.editorHeight, custom.legacyEditorPercent)
	}
}

func TestDatabasePagingFooter(t *testing.T) {
	project, path := databaseFixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE pages(id INTEGER PRIMARY KEY); WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<105) INSERT INTO pages(id) SELECT x FROM n`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	d := a.focused().database
	d.schema, d.table = "main", "pages"
	d.results[databaseData].offset = 0
	a.loadDatabaseRows(a.focused(), true)
	tt := ui.NewTester(a.view, 1400, 800)
	tt.Frame()
	if !tt.HasText("Rows 1–100") || !tt.HasText("More rows available") {
		t.Fatalf("first page footer: %q", tt.Texts())
	}
	dark := ui.NewTester(func(c *ui.Context) { c.SetTheme(ui.DarkTheme()); a.view(c) }, 1400, 800)
	dark.Frame()
	snapshot(t, dark, "database-rows-dark.png")
	if err := tt.Click("Next page"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("Rows 101–105") || len(d.results[databaseData].result.Rows) != 5 {
		t.Fatalf("second page footer: %q", tt.Texts())
	}
	if err := tt.Click("Rows per page"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("25"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.pageSize != 25 || d.results[databaseData].offset != 0 || !tt.HasText("Rows 1–25") {
		t.Fatalf("page size reset: size %d, offset %d, texts %q", d.pageSize, d.results[databaseData].offset, tt.Texts())
	}
	if got := saveDatabaseView(d).PageSize; got != 25 {
		t.Fatalf("saved page size: %d", got)
	}
	d.results[databaseQuery].result = database.Result{Columns: []string{"value"}, Rows: [][]any{{1}}, Truncated: true}
	d.activeResult = databaseQuery
	tt.Frame()
	if !tt.HasText("Truncated at row or 1 MiB limit") {
		t.Fatalf("query truncation footer: %q", tt.Texts())
	}
}

func TestDatabaseResultScrollsHorizontally(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	d := a.focused().database
	d.results[databaseData].result = database.Result{Columns: []string{"first", "second", "third", "fourth", "fifth", "last_col"}, Rows: [][]any{{1, 2, 3, 4, 5, 6}}}
	tt := ui.NewTester(a.view, 1200, 700)
	tt.Frame()
	before, ok := tt.Find("last_col")
	if !ok {
		t.Fatal("last column header missing")
	}
	tt.Scroll(850, 450, 400, 0)
	after, ok := tt.Find("last_col")
	if !ok || after.X >= before.X {
		t.Fatalf("horizontal scroll did not move last column: before=%v after=%v", before.X, after.X)
	}
}

func TestDatabaseSidebarOpensSavedConnection(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	tt := ui.NewTester(a.view, 1200, 700)
	tt.Frame()
	if err := tt.Click("local"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.tabs) != 1 || a.focused() == nil || a.focused().database == nil || a.focused().database.profile != "local" {
		t.Fatalf("sidebar did not open connection: %+v", a.tabs)
	}
}

func TestDatabaseGlobalStoreKeepsURLInEnvironment(t *testing.T) {
	project, _ := databaseFixture(t)
	t.Setenv("LARIK_DESKTOP_DB_URL", "postgres://user:secret@localhost/example")
	legacy := config.LocalSettingsPath(project)
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte(`{"database_connections":{"old":{"driver":"sqlite","path":"old.db"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Cwd: project})
	defer a.Close()
	if len(a.databaseStore.names()) != 0 {
		t.Fatal("legacy project profile was imported")
	}
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"remote": {Driver: "postgres", URLEnv: "LARIK_DESKTOP_DB_URL"}}); err != nil {
		t.Fatal(err)
	}
	if got := a.databaseStore.maskedEnv(); len(got) != 1 || got[0] != "LARIK_DESKTOP_DB_URL=" {
		t.Fatalf("terminal environment: %v", got)
	}
	data, err := os.ReadFile(config.DesktopDatabasePath())
	if err != nil || strings.Contains(string(data), "secret") || !strings.Contains(string(data), "LARIK_DESKTOP_DB_URL") {
		t.Fatalf("saved credential: %v %s", err, data)
	}
	other := New(Options{Cwd: t.TempDir()})
	defer other.Close()
	if _, ok := other.databaseStore.get("remote"); !ok {
		t.Fatal("connection was scoped to project")
	}
}

func TestControlDatabasePane(t *testing.T) {
	project, _ := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	res := a.command(Request{Cmd: "open", Cwd: project, Database: true})
	if res.Error != "" || res.Tab == nil || res.Tab.Kind != "database" {
		t.Fatalf("open --database: %+v", res)
	}
	for _, cmd := range []string{"send", "read"} {
		if got := a.command(Request{Cmd: cmd, Tab: res.Tab.ID, Text: "hi"}); got.Error != "a Database tab has no terminal" {
			t.Fatalf("%s: %+v", cmd, got)
		}
	}
	if got := a.command(Request{Cmd: "close", Tab: res.Tab.ID}); got.Error != "" || len(a.tabs) != 0 {
		t.Fatalf("close: %+v", got)
	}
}
