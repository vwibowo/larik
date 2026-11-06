package desktop

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
	"larik/internal/database"
)

// DatabasePaneInfo names one open pane whose selected connection is shared
// with the agent. Credentials and connection paths never enter this response.
type DatabasePaneInfo struct {
	PaneID     string `json:"pane_id"`
	Connection string `json:"connection"`
	Driver     string `json:"driver"`
}

type DatabaseResponse struct {
	Panes  []DatabasePaneInfo `json:"panes,omitempty"`
	Tables []database.Table   `json:"tables,omitempty"`
	Result *database.Result   `json:"result,omitempty"`
}

func (a *App) eligibleDatabasePanes() []DatabasePaneInfo {
	var out []DatabasePaneInfo
	for _, tb := range a.tabs {
		if tb.database == nil || tb.database.profile == "" || a.paneOf(tb) == nil {
			continue
		}
		p, ok := a.databaseStore.get(tb.database.profile)
		if ok && p.AgentAccess {
			out = append(out, DatabasePaneInfo{tb.id, tb.database.profile, p.Driver})
		}
	}
	return out
}

func (a *App) databaseControl(req Request) Response {
	var tb *tab
	var profile string
	var seq uint64
	var ctx context.Context
	var manager *database.Manager
	var listed []DatabasePaneInfo
	var problem string
	if !a.onMain(func() {
		listed = a.eligibleDatabasePanes()
		if req.Op == "list" {
			return
		}
		if req.Tab != "" {
			tb = a.tabByID(req.Tab)
		} else if len(listed) == 1 {
			tb = a.tabByID(listed[0].PaneID)
		} else {
			problem = "choose an open, agent-enabled Database pane ID"
			return
		}
		if tb == nil || tb.database == nil || a.paneOf(tb) == nil {
			problem = "Database pane is not open"
			return
		}
		profile = tb.database.profile
		p, ok := a.databaseStore.get(profile)
		if !ok || !p.AgentAccess {
			problem = "Database connection is not available to the agent"
			return
		}
		manager = a.databaseStore.manager
		if manager == nil {
			problem = "Database connections are unavailable"
			return
		}
		seq = tb.database.seq
		ctx = tb.database.ctx
	}) {
		return Response{Error: "Larik Desktop is closing"}
	}
	if req.Op == "list" {
		return Response{Database: &DatabaseResponse{Panes: listed}}
	}
	if problem != "" {
		return Response{Error: problem}
	}
	start := time.Now()
	res := &DatabaseResponse{}
	var err error
	switch req.Op {
	case "schema":
		res.Tables, err = manager.Schema(ctx, profile, true)
	case "rows":
		var result database.Result
		result, err = manager.Rows(ctx, profile, req.Schema, req.Table, req.Offset, req.Limit, true)
		res.Result = &result
	case "query":
		var result database.Result
		result, err = manager.Query(ctx, profile, req.SQL, req.Limit, true)
		res.Result = &result
	default:
		return Response{Error: fmt.Sprintf("unknown Database operation %q", req.Op)}
	}
	if err != nil {
		return Response{Error: err.Error()}
	}
	// Recheck access after I/O so a revoked or closed pane never returns data.
	valid := false
	if !a.onMain(func() {
		p, ok := a.databaseStore.get(profile)
		if !ok || !p.AgentAccess || tb.database == nil || tb.database.ctx.Err() != nil || tb.database.seq != seq || tb.database.profile != profile || a.paneOf(tb) == nil {
			return
		}
		valid = true
		d := tb.database
		d.status = ""
		if req.Op == "schema" {
			d.tables = res.Tables
		}
		if res.Result != nil {
			v := &d.results[databaseAgent]
			v.result, v.selectedRow, v.elapsed, v.status = *res.Result, -1, time.Since(start), ""
			v.selection.Clear()
			v.grid.Cells = ui.ColumnRange{}
			v.source = "SQL query"
			if req.Op == "rows" {
				v.source = req.Schema + "." + req.Table
				v.offset = req.Offset
			} else {
				v.offset = 0
			}
			d.selectResult(databaseAgent)
		}
	}) || !valid {
		return Response{Error: "Database pane is no longer available to the agent"}
	}
	return Response{Database: res}
}
