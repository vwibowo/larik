package desktopdb

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/database"
	"larik/internal/desktop"
)

func TestDesktopDatabaseToolsUsePaneBridge(t *testing.T) {
	var called desktop.Request
	all := ToolsWith(func(req desktop.Request) (desktop.Response, error) {
		called = req
		return desktop.Response{Database: &desktop.DatabaseResponse{Result: &database.Result{Columns: []string{"name"}, Rows: [][]any{{"hello"}}}}}, nil
	})
	if len(all) != 4 {
		t.Fatalf("%d tools", len(all))
	}
	for i, name := range []string{"desktop_db_list", "desktop_db_schema", "desktop_db_rows", "desktop_db_query"} {
		if all[i].Spec().Name != name || !all[i].ReadOnly() {
			t.Fatalf("tool %d: %+v", i, all[i].Spec())
		}
	}
	result := all[3].Run(context.Background(), nil, json.RawMessage(`{"pane_id":"pane-1","sql":"SELECT name FROM items","limit":2}`))
	if result.IsError || !strings.Contains(result.Content, "hello") || called.Cmd != "database" || called.Op != "query" || called.Tab != "pane-1" || called.Limit != 2 {
		t.Fatalf("bridge result: %+v, request: %+v", result, called)
	}
	if result := all[2].Run(context.Background(), nil, json.RawMessage(`{`)); !result.IsError {
		t.Fatal("invalid input accepted")
	}
}
