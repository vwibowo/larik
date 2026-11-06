package desktop

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"larik/internal/database"
)

func completionFixture() *databaseState {
	d := &databaseState{
		sql:    &editor.State{Buffer: editor.NewBuffer("")},
		schema: "main", table: "notes",
		tables: []database.Table{
			{Schema: "main", Name: "notes", Type: "table", Columns: []database.Column{{Name: "id", Type: "integer"}, {Name: "body", Type: "text"}}},
			{Schema: "analytics", Name: "order items", Type: "table", Columns: []database.Column{{Name: "line total", Type: "numeric"}}},
		},
	}
	d.sql.Buffer.SetLanguage(editor.LanguageNamed("sql"))
	return d
}

func completionsAt(d *databaseState, text string, caret int, prefix string, explicit bool) []editor.Completion {
	d.sql.Buffer.Set(text)
	return databaseCompletions(d, editor.CompletionRequest{Offset: caret, Prefix: prefix, Version: d.sql.Buffer.Version(), Explicit: explicit})
}

func findCompletion(items []editor.Completion, label string) (editor.Completion, bool) {
	for _, item := range items {
		if item.Label == label {
			return item, true
		}
	}
	return editor.Completion{}, false
}

func TestDatabaseSQLCompletions(t *testing.T) {
	d := completionFixture()
	for _, tc := range []struct {
		name, text, prefix, want, insert string
		caret                            int
		explicit                         bool
	}{
		{"keyword", "sel", "sel", "SELECT", "SELECT", 3, false},
		{"table after FROM", "SELECT * FROM no", "no", "notes", "notes", len("SELECT * FROM no"), false},
		{"table after JOIN", "SELECT * FROM notes JOIN or", "or", "order items", `analytics."order items"`, len("SELECT * FROM notes JOIN or"), false},
		{"schema after FROM", "SELECT * FROM ana", "ana", "analytics", "analytics", len("SELECT * FROM ana"), false},
		{"schema table", "SELECT * FROM analytics.", "", "order items", `"order items"`, len("SELECT * FROM analytics."), false},
		{"alias column", "SELECT n. FROM notes n", "", "body", "body", len("SELECT n."), false},
		{"selected table column", "SELECT bo", "bo", "body", "body", len("SELECT bo"), false},
		{"quoted column", `SELECT oi. FROM analytics."order items" oi`, "", "line total", `"line total"`, len("SELECT oi."), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item, ok := findCompletion(completionsAt(d, tc.text, tc.caret, tc.prefix, tc.explicit), tc.want)
			if !ok || item.Insert != tc.insert {
				t.Fatalf("completion %q: got %+v, found=%v", tc.want, item, ok)
			}
		})
	}
	if _, ok := findCompletion(completionsAt(d, "in", 2, "in", false), "INSERT"); ok {
		t.Fatal("write keyword offered")
	}
	if items := completionsAt(d, "s", 1, "s", false); len(items) != 0 {
		t.Fatalf("one-character automatic completion: %+v", items)
	}
	if items := completionsAt(d, "", 0, "", true); len(items) == 0 || len(items) > 50 {
		t.Fatalf("explicit completion count: %d", len(items))
	}
	if got := databaseSQLIdentifier("table"); got != `"table"` {
		t.Fatalf("reserved table name insert: %q", got)
	}
	d.tables = nil
	if _, ok := findCompletion(completionsAt(d, "sel", 3, "sel", false), "SELECT"); !ok {
		t.Fatal("keyword completion unavailable before schema loads")
	}
	if items := completionsAt(d, "SELECT * FROM no", len("SELECT * FROM no"), "no", false); len(items) != 0 {
		t.Fatalf("schema completion offered before schema loads: %+v", items)
	}
}

func TestDatabaseSQLCompletionSuppression(t *testing.T) {
	d := completionFixture()
	for _, text := range []string{"SELECT 'sel", "SELECT -- sel", "SELECT /* sel", `SELECT "sel`} {
		if items := completionsAt(d, text, len(text), "sel", true); len(items) != 0 {
			t.Errorf("completion inside quoted text or comment %q: %+v", text, items)
		}
	}
	text := "SELECT -- comment\nsel"
	if _, ok := findCompletion(completionsAt(d, text, len(text), "sel", false), "SELECT"); !ok {
		t.Fatal("completion did not recover after line comment")
	}
	d.sql.Buffer.Set("sel")
	if items := databaseCompletions(d, editor.CompletionRequest{Offset: 3, Prefix: "sel", Version: d.sql.Buffer.Version() - 1}); len(items) != 0 {
		t.Fatal("stale completion accepted")
	}
}

func TestDatabaseSQLDialectCompletions(t *testing.T) {
	d := completionFixture()
	d.driver = "sqlserver"
	if item, ok := findCompletion(completionsAt(d, "SELECT * FROM analytics.", len("SELECT * FROM analytics."), "", false), "order items"); !ok || item.Insert != "[order items]" {
		t.Fatalf("SQL Server table completion: %+v %v", item, ok)
	}
	if item, ok := findCompletion(completionsAt(d, "to", 2, "to", false), "TOP"); !ok || item.Insert != "TOP" {
		t.Fatalf("SQL Server TOP completion: %+v %v", item, ok)
	}
	if _, ok := findCompletion(completionsAt(d, "li", 2, "li", false), "LIMIT"); ok {
		t.Fatal("SQL Server offered LIMIT")
	}
	d.driver = "mysql"
	if item, ok := findCompletion(completionsAt(d, "SELECT * FROM analytics.", len("SELECT * FROM analytics."), "", false), "order items"); !ok || item.Insert != "`order items`" {
		t.Fatalf("MySQL table completion: %+v %v", item, ok)
	}
	if items := completionsAt(d, "SELECT `ord", len("SELECT `ord"), "ord", true); len(items) != 0 {
		t.Fatalf("completion in quoted MySQL identifier: %+v", items)
	}
}

func TestDatabaseSQLEditorKeyboardAndTheme(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	d := a.focused().database
	if d.sql.Buffer.Language() != editor.LanguageNamed("sql") {
		t.Fatal("SQL syntax highlighting is not enabled")
	}
	light := ui.NewTester(a.view, 1400, 800)
	light.Frame()
	if d.sql.Theme == nil || d.sql.Theme.Tokens[editor.TokenKeyword].Color != a.palette(ui.LightTheme()).Accent {
		t.Fatal("SQL keywords do not follow the light theme accent")
	}
	if err := light.Click("Read-only SQL query"); err != nil {
		t.Fatal(err)
	}
	light.Type("sel")
	light.Frame()
	snapshot(t, light, "database-completions.png")
	if !light.HasText("SELECT") {
		t.Fatalf("automatic completion popup: %q", light.Texts())
	}
	light.Key(0, ui.KeyEnter)
	if got := d.sql.Buffer.String(); got != "SELECT" {
		t.Fatalf("accepted completion: %q", got)
	}
	d.sql.Buffer.Set("CO")
	d.sql.Select(2, 2)
	light.Frame()
	light.Key(ui.Ctrl, ui.KeySpace)
	if !light.HasText("COALESCE") {
		t.Fatal("Ctrl+Space did not open completions")
	}
	light.Key(0, ui.KeyEscape)
	if d.sql.Buffer.String() != "CO" {
		t.Fatal("Escape changed the SQL draft")
	}
	light.Key(ui.Ctrl, ui.KeySpace)
	light.Key(0, ui.KeyTab)
	if got := d.sql.Buffer.String(); got == "CO" || !strings.HasPrefix(got, "CO") {
		t.Fatalf("Tab did not accept completion: %q", d.sql.Buffer.String())
	}
	d.sql.Buffer.Set("SELECT")
	d.sql.Select(len("SELECT"), len("SELECT"))
	light.Frame()
	light.Type(" * FROM notes\nWHERE id = 1")
	if !strings.Contains(d.sql.Buffer.String(), "\nWHERE") {
		t.Fatal("multiline SQL editing failed")
	}
	light.Key(ui.Cmd, ui.KeyZ)
	if strings.Contains(d.sql.Buffer.String(), "WHERE") {
		t.Fatal("SQL editor undo did not remove the last edit")
	}
	d.sql.Buffer.Set("SELECT count(*) FROM notes")
	light.Key(ui.Ctrl, ui.KeyEnter)
	if d.activeResult != databaseQuery || len(d.results[databaseQuery].result.Rows) != 1 {
		t.Fatal("Ctrl+Enter did not run the editor buffer")
	}
	snapshot(t, light, "database-editor-light.png")
	query := d.sql.Buffer.String()
	d.selectResult(databaseData)
	d.selectResult(databaseAgent)
	if d.sql.Buffer.String() != query {
		t.Fatal("switching result tabs changed the SQL draft")
	}
	d.selectResult(databaseQuery)
	dark := ui.NewTester(func(c *ui.Context) { c.SetTheme(ui.DarkTheme()); a.view(c) }, 1400, 800)
	dark.Frame()
	snapshot(t, dark, "database-editor-dark.png")
	if d.sql.Theme == nil || d.sql.Theme.Tokens[editor.TokenKeyword].Color != a.palette(ui.DarkTheme()).Accent || !a.palette(ui.DarkTheme()).Dark {
		t.Fatal("SQL keywords do not follow the dark theme accent")
	}
}
