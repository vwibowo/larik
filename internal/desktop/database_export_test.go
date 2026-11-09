package desktop

import (
	"encoding/csv"
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"larik/internal/database"
)

func TestDatabaseCSVLoadedRows(t *testing.T) {
	r := database.Result{Columns: []string{"name", "note", "missing"}, Rows: [][]any{{"a,b", "line\n\"two\"", nil}, {"NULL", "", "ok"}}, Truncated: true}
	data, err := databaseCSV(r)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || len(rows) != 3 || rows[1][0] != "a,b" || rows[1][1] != "line\n\"two\"" || rows[1][2] != "NULL" || rows[2][0] != "NULL" || rows[2][1] != "" {
		t.Fatalf("CSV rows %q, error %v", rows, err)
	}
}

func TestDatabaseGridCopyAndExport(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	tb := a.focused()
	v := &tb.database.results[databaseData]
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
	if v.selectedRow != 0 || v.selection.Len() != 1 {
		t.Fatalf("cell selection: row %d, count %d", v.selectedRow, v.selection.Len())
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
	v.result.Rows[1][1] = "line\nwith\ttab"
	if got := databaseTSV(v, false); got != "hello\n\"line\nwith\ttab\"" {
		t.Fatalf("escaped TSV range: %q", got)
	}
	v.result.Rows[1][1] = "world"
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
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	tb := a.focused()
	v := &tb.database.results[databaseData]
	a.saveDatabaseCSV = func(_ string, _ []byte, done func(string, error)) { done("", nil) }
	a.exportDatabaseResult(tb)
	if v.status != "" {
		t.Fatalf("canceled export status: %q", v.status)
	}
	a.saveDatabaseCSV = func(_ string, _ []byte, done func(string, error)) { done("", errors.New("disk full")) }
	a.exportDatabaseResult(tb)
	if v.status != "Export failed: disk full" {
		t.Fatalf("failed export status: %q", v.status)
	}
}

func TestDatabaseObjectExportUsesLoadedPageOrLoadsFirstPage(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
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
	loaded := &tb.database.results[databaseData]
	loaded.source = "main.notes"
	loaded.result = database.Result{Columns: []string{"body"}, Rows: [][]any{{"current page"}}}
	a.exportDatabaseObject(tb, object)
	if exportedName != "notes.csv" || !strings.Contains(exportedCSV, "body\ncurrent page\n") {
		t.Fatalf("loaded page export %q: %q", exportedName, exportedCSV)
	}

	exportedName, exportedCSV = "", ""
	loaded.source = ""
	a.exportDatabaseObject(tb, object)
	if exportedName != "notes.csv" || !strings.Contains(exportedCSV, "id,body\n1,hello\n") {
		t.Fatalf("first page export %q: %q", exportedName, exportedCSV)
	}
}
