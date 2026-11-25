package desktop

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"larik/internal/database"
)

func TestDatabaseGridCopyAndExport(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.Save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	tb := a.focused()
	v := &tb.databaseState().Results[databaseData]
	var exportedName, exportedCSV string
	a.saveDatabaseCSV = func(name string, data []byte, done func(string, error)) {
		exportedName, exportedCSV = name, string(data)
		done(name, nil)
	}
	tt := ui.NewTester(a.view, 1400, 800)
	tt.Frame()
	if err := tt.Click("hello"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyC)
	if got := tt.Clipboard(); got != "hello" {
		t.Fatalf("copied cell %q, want hello", got)
	}
	if v.SelectedRow != 0 || v.Selection.Len() != 1 {
		t.Fatalf("cell selection: row %d, count %d", v.SelectedRow, v.Selection.Len())
	}
	if err := tt.Click("Copy selected cells"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Copy with headers"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != "body\nhello" {
		t.Fatalf("copy with headers: %q", got)
	}
	if err := tt.ClickWith(ui.Shift, "world"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyC)
	if got := tt.Clipboard(); got != "hello\nworld" {
		t.Fatalf("copied range: %q", got)
	}
	v.Result.Rows[1][1] = "line\nwith\ttab"
	if got := databaseTSV(v, false); got != "hello\n\"line\nwith\ttab\"" {
		t.Fatalf("escaped TSV range: %q", got)
	}
	v.Result.Rows[1][1] = "world"
	if err := tt.Click("Export loaded rows as CSV"); err != nil {
		t.Fatal(err)
	}
	if exportedName != "notes.csv" || !strings.Contains(exportedCSV, "id,body\n") || !strings.Contains(exportedCSV, "1,hello\n") {
		t.Fatalf("export %q: %q", exportedName, exportedCSV)
	}
	if err := tt.Click("world"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("world"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Copy body"); err != nil {
		t.Fatal(err)
	}
	if got := tt.Clipboard(); got != "world" {
		t.Fatalf("inspector copy: %q", got)
	}
}

func TestDatabaseExportCancelAndFailure(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.Save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	tb := a.focused()
	v := &tb.databaseState().Results[databaseData]
	a.saveDatabaseCSV = func(_ string, _ []byte, done func(string, error)) { done("", nil) }
	a.exportDatabaseResult(tb)
	if v.Status != "" {
		t.Fatalf("canceled export status: %q", v.Status)
	}
	a.saveDatabaseCSV = func(_ string, _ []byte, done func(string, error)) { done("", errors.New("disk full")) }
	a.exportDatabaseResult(tb)
	if v.Status != "Export failed: disk full" {
		t.Fatalf("failed export status: %q", v.Status)
	}
}

func TestDatabaseObjectExportUsesLoadedPageOrLoadsFirstPage(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.Save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	tb := a.focused()
	var exportedName, exportedCSV string
	a.saveDatabaseCSV = func(name string, data []byte, done func(string, error)) {
		exportedName, exportedCSV = name, string(data)
		done(name, nil)
	}
	object := database.Table{Schema: "main", Name: "notes", Type: "table"}
	loaded := &tb.databaseState().Results[databaseData]
	loaded.Source = "main.notes"
	loaded.Result = database.Result{Columns: []string{"body"}, Rows: [][]any{{"current page"}}}
	a.exportDatabaseObject(tb, object)
	if exportedName != "notes.csv" || !strings.Contains(exportedCSV, "body\ncurrent page\n") {
		t.Fatalf("loaded page export %q: %q", exportedName, exportedCSV)
	}

	exportedName, exportedCSV = "", ""
	loaded.Source = ""
	a.exportDatabaseObject(tb, object)
	if exportedName != "notes.csv" || !strings.Contains(exportedCSV, "id,body\n1,hello\n") {
		t.Fatalf("first page export %q: %q", exportedName, exportedCSV)
	}
}
