package desktop

import (
	"database/sql"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"larik/internal/database"
)

func TestDatabaseObjectContextMenusAndStructure(t *testing.T) {
	project, path := databaseFixture(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE VIEW note_view AS SELECT id, body FROM notes`); err != nil {
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
	tb := a.focused()
	tb.database.sql.Buffer.Set("SELECT 'draft'")
	var copied string
	a.copyText = func(text string) { copied = text }
	var exportedName, exportedCSV string
	a.saveDatabaseCSV = func(name string, data []byte, done func(string, error)) {
		exportedName, exportedCSV = name, string(data)
		done(name, nil)
	}
	tt := ui.NewTester(a.view, 1400, 800)
	tt.Frame()

	rightClick := func(label string) []string {
		t.Helper()
		if err := tt.RightClick(label); err != nil {
			t.Fatal(err)
		}
		return tt.Menu()
	}
	choose := func(item ...string) {
		t.Helper()
		if err := tt.ChooseMenuItem(item...); err != nil {
			t.Fatal(err, tt.Menu())
		}
		tt.Frame()
	}

	menu := rightClick("▦  notes")
	for _, item := range []string{"Open", "Open Structure", "Copy Name", "Copy Script As", "Export"} {
		if !slices.Contains(menu, item) {
			t.Fatalf("table menu lacks %q: %v", item, menu)
		}
	}
	choose("Copy Name")
	if copied != "notes" {
		t.Fatalf("copied table name %q", copied)
	}
	menu = rightClick("▦  notes")
	_ = menu
	choose("Copy Script As", "SELECT statement")
	if copied != `SELECT * FROM "main"."notes";` {
		t.Fatalf("copied SELECT %q", copied)
	}
	rightClick("▦  notes")
	choose("Copy Script As", "Creation")
	if copied != `CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT)` {
		t.Fatalf("copied table creation script %q", copied)
	}
	rightClick("▦  notes")
	choose("Export", "CSV")
	if exportedName != "notes.csv" || exportedCSV == "" {
		t.Fatalf("context export %q: %q", exportedName, exportedCSV)
	}

	rightClick("▦  notes")
	choose("Open Structure")
	if !tb.database.showStructure || !tt.HasText("Structure") || !tt.HasText("body") || !tt.HasText("TEXT") || !tt.HasText("Primary") {
		t.Fatalf("structure view missing details: %q", tt.Texts())
	}
	if got := tb.database.sql.Buffer.String(); got != "SELECT 'draft'" {
		t.Fatalf("structure changed SQL draft: %q", got)
	}
	if err := tt.Click("Back to table rows"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tb.database.showStructure || !tt.HasText("SQL query") || !tt.HasText("hello") {
		t.Fatalf("back to rows: %q", tt.Texts())
	}
	if got := tb.database.sql.Buffer.String(); got != "SELECT 'draft'" {
		t.Fatalf("returning to rows changed SQL draft: %q", got)
	}
	rightClick("▦  notes")
	choose("Open")
	if !tt.HasText("hello") || tb.database.showStructure || tb.database.showFunctionDefinition {
		t.Fatalf("open rows: %q", tt.Texts())
	}

	menu = rightClick("◫  note_view")
	if !slices.Contains(menu, "Open Structure") || !slices.Contains(menu, "Export") {
		t.Fatalf("view menu: %v", menu)
	}
	choose("Copy Script As", "Creation")
	if copied != `CREATE VIEW note_view AS SELECT id, body FROM notes` {
		t.Fatalf("copied view creation script %q", copied)
	}
	tb.database.tables = append(tb.database.tables, database.Table{Schema: "main", Name: "rebuild_index", Type: "FUNCTION"})
	tt.Frame()
	menu = rightClick("ƒ  rebuild_index")
	for _, item := range []string{"Open Definition", "Copy Name", "Copy Script As"} {
		if !slices.Contains(menu, item) {
			t.Fatalf("function menu lacks %q: %v", item, menu)
		}
	}
	if slices.Contains(menu, "Open") || slices.Contains(menu, "Open Structure") || slices.Contains(menu, "Export") {
		t.Fatalf("function menu contains table-only actions: %v", menu)
	}
}
