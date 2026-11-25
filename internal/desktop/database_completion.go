package desktop

import (
	"github.com/egoist/mygo/ui/editor"
	"larik/internal/database"
)

var databaseCompletion = func(string, []database.Table, string, string, *editor.State, editor.CompletionRequest) []editor.Completion {
	return nil
}
var databaseIdentifier = func(name string) string { return name }

func databaseCompletions(d *databaseState, req editor.CompletionRequest) []editor.Completion {
	if d == nil {
		return nil
	}
	return databaseCompletion(d.Driver, d.Tables, d.Schema, d.Table, d.SQL, req)
}

func databaseSQLIdentifier(name string) string { return databaseIdentifier(name) }
