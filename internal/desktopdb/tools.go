// Package desktopdb exposes read-only Database pane operations to an agent
// running inside Larik Desktop. The Desktop process owns connections.
package desktopdb

import (
	"context"
	"encoding/json"
	"fmt"

	"larik/internal/desktop"
	"larik/internal/llm"
	"larik/internal/tools"
)

type Caller func(desktop.Request) (desktop.Response, error)
type tool struct {
	name string
	call Caller
}

func Tools(dataDir string) []tools.Tool {
	return ToolsWith(func(req desktop.Request) (desktop.Response, error) { return desktop.Call(dataDir, req) })
}
func ToolsWith(call Caller) []tools.Tool {
	return []tools.Tool{tool{"list", call}, tool{"schema", call}, tool{"rows", call}, tool{"query", call}}
}

func (t tool) ReadOnly() bool { return true }
func (t tool) Spec() llm.ToolSpec {
	var desc, schema string
	switch t.name {
	case "list":
		desc = "List open Larik Desktop Database panes whose selected connections are shared with the agent. Returns pane IDs, names and drivers, never credentials."
		schema = `{"type":"object","properties":{}}`
	case "schema":
		desc = "Inspect schemas, tables, views and columns through an open, agent-enabled Larik Desktop Database pane."
		schema = `{"type":"object","properties":{"pane_id":{"type":"string"}}}`
	case "rows":
		desc = "Read a page of table rows through an open, agent-enabled Larik Desktop Database pane. At most 500 rows and 1 MiB."
		schema = `{"type":"object","properties":{"pane_id":{"type":"string"},"schema":{"type":"string"},"table":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["schema","table"]}`
	case "query":
		desc = "Run one read-only SQL query through an open, agent-enabled Larik Desktop Database pane. At most 500 rows, 1 MiB and 10 seconds."
		schema = `{"type":"object","properties":{"pane_id":{"type":"string"},"sql":{"type":"string"},"limit":{"type":"integer"}},"required":["sql"]}`
	}
	return llm.ToolSpec{Name: "desktop_db_" + t.name, Description: desc, Schema: json.RawMessage(schema)}
}
func (t tool) Run(_ context.Context, _ *tools.Env, input json.RawMessage) tools.Result {
	var in struct {
		PaneID string `json:"pane_id"`
		Schema string `json:"schema"`
		Table  string `json:"table"`
		SQL    string `json:"sql"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if len(input) > 0 && json.Unmarshal(input, &in) != nil {
		return tools.Result{Content: "invalid arguments", IsError: true}
	}
	res, err := t.call(desktop.Request{Cmd: "database", Op: t.name, Tab: in.PaneID, Schema: in.Schema, Table: in.Table, SQL: in.SQL, Offset: in.Offset, Limit: in.Limit})
	if err != nil {
		return tools.Result{Content: err.Error(), IsError: true}
	}
	if res.Error != "" {
		return tools.Result{Content: res.Error, IsError: true}
	}
	if res.Database == nil {
		return tools.Result{Content: fmt.Sprintf("Desktop did not return a Database result for %s", t.name), IsError: true}
	}
	b, err := json.Marshal(res.Database)
	if err != nil {
		return tools.Result{Content: "could not encode Database result", IsError: true}
	}
	return tools.Result{Content: string(b), Data: res.Database}
}
