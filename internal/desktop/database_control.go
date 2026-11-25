package desktop

import (
	"context"
	"errors"
	"time"

	"github.com/egoist/mygo/ui"
	"larik/internal/database"
	"larik/internal/desktop/panes"
)

var databaseExecute = func(context.Context, *database.Manager, string, panes.DatabaseControlRequest) ([]database.Table, *database.Result, error) {
	return nil, nil, errors.New("Database panes are unavailable")
}

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
		if tb.databaseState() == nil || tb.databaseState().Profile == "" || a.paneOf(tb) == nil {
			continue
		}
		p, ok := a.databaseStore.Get(tb.databaseState().Profile)
		if ok && p.AgentAccess {
			out = append(out, DatabasePaneInfo{tb.id, tb.databaseState().Profile, p.Driver})
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
		if tb == nil || tb.databaseState() == nil || a.paneOf(tb) == nil {
			problem = "Database pane is not open"
			return
		}
		profile = tb.databaseState().Profile
		p, ok := a.databaseStore.Get(profile)
		if !ok || !p.AgentAccess {
			problem = "Database connection is not available to the agent"
			return
		}
		manager = a.databaseStore.Manager()
		if manager == nil {
			problem = "Database connections are unavailable"
			return
		}
		seq = tb.databaseState().Seq
		ctx = tb.databaseState().Ctx
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
	res.Tables, res.Result, err = databaseExecute(ctx, manager, profile, panes.DatabaseControlRequest{Op: req.Op, Schema: req.Schema, Table: req.Table, SQL: req.SQL, Offset: req.Offset, Limit: req.Limit})
	if err != nil {
		return Response{Error: err.Error()}
	}
	// Recheck access after I/O so a revoked or closed pane never returns data.
	valid := false
	if !a.onMain(func() {
		p, ok := a.databaseStore.Get(profile)
		if !ok || !p.AgentAccess || tb.databaseState() == nil || tb.databaseState().Ctx.Err() != nil || tb.databaseState().Seq != seq || tb.databaseState().Profile != profile || a.paneOf(tb) == nil {
			return
		}
		valid = true
		d := tb.databaseState()
		d.Status = ""
		if req.Op == "schema" {
			d.Tables = res.Tables
		}
		if res.Result != nil {
			v := &d.Results[databaseAgent]
			v.Result, v.SelectedRow, v.Elapsed, v.Status = *res.Result, -1, time.Since(start), ""
			v.Selection.Clear()
			v.Grid.Cells = ui.ColumnRange{}
			v.Source = "SQL query"
			if req.Op == "rows" {
				v.Source = req.Schema + "." + req.Table
				v.Offset = req.Offset
			} else {
				v.Offset = 0
			}
			d.SelectResult(databaseAgent)
		}
	}) || !valid {
		return Response{Error: "Database pane is no longer available to the agent"}
	}
	return Response{Database: res}
}
