package desktop

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"larik/internal/database"
)

func TestDatabaseSQLEditorKeyboardAndTheme(t *testing.T) {
	project, path := databaseFixture(t)
	a := New(Options{Cwd: project})
	defer a.Close()
	if err := a.databaseStore.Save(map[string]database.ConnectionConfig{"local": {Driver: "sqlite", Path: path}}); err != nil {
		t.Fatal(err)
	}
	a.openDatabaseProfile("local")
	d := a.focused().databaseState()
	if d.SQL.Buffer.Language() != editor.LanguageNamed("sql") {
		t.Fatal("SQL syntax highlighting is not enabled")
	}
	light := ui.NewTester(a.view, 1400, 800)
	light.Frame()
	if d.SQL.Theme == nil || d.SQL.Theme.Tokens[editor.TokenKeyword].Color != a.palette(ui.LightTheme()).Accent {
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
	if got := d.SQL.Buffer.String(); got != "SELECT" {
		t.Fatalf("accepted completion: %q", got)
	}
	d.SQL.Buffer.Set("CO")
	d.SQL.Select(2, 2)
	light.Frame()
	light.Key(ui.Ctrl, ui.KeySpace)
	if !light.HasText("COALESCE") {
		t.Fatal("Ctrl+Space did not open completions")
	}
	light.Key(0, ui.KeyEscape)
	if d.SQL.Buffer.String() != "CO" {
		t.Fatal("Escape changed the SQL draft")
	}
	light.Key(ui.Ctrl, ui.KeySpace)
	light.Key(0, ui.KeyTab)
	if got := d.SQL.Buffer.String(); got == "CO" || !strings.HasPrefix(got, "CO") {
		t.Fatalf("Tab did not accept completion: %q", d.SQL.Buffer.String())
	}
	d.SQL.Buffer.Set("SELECT")
	d.SQL.Select(len("SELECT"), len("SELECT"))
	light.Frame()
	light.Type(" * FROM notes\nWHERE id = 1")
	if !strings.Contains(d.SQL.Buffer.String(), "\nWHERE") {
		t.Fatal("multiline SQL editing failed")
	}
	light.Key(ui.Cmd, ui.KeyZ)
	if strings.Contains(d.SQL.Buffer.String(), "WHERE") {
		t.Fatal("SQL editor undo did not remove the last edit")
	}
	d.SQL.Buffer.Set("SELECT count(*) FROM notes")
	light.Key(ui.Cmd, ui.KeyEnter)
	if d.ActiveResult != databaseQuery || len(d.Results[databaseQuery].Result.Rows) != 1 {
		t.Fatal("Cmd+Enter did not run the editor buffer")
	}
	snapshot(t, light, "database-editor-light.png")
	query := d.SQL.Buffer.String()
	d.SelectResult(databaseData)
	d.SelectResult(databaseAgent)
	if d.SQL.Buffer.String() != query {
		t.Fatal("switching result tabs changed the SQL draft")
	}
	d.SelectResult(databaseQuery)
	dark := ui.NewTester(func(c *ui.Context) { c.SetTheme(ui.DarkTheme()); a.view(c) }, 1400, 800)
	dark.Frame()
	snapshot(t, dark, "database-editor-dark.png")
	if d.SQL.Theme == nil || d.SQL.Theme.Tokens[editor.TokenKeyword].Color != a.palette(ui.DarkTheme()).Accent || !a.palette(ui.DarkTheme()).Dark {
		t.Fatal("SQL keywords do not follow the dark theme accent")
	}
}
