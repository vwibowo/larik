package databasepane

import (
	"context"
	"fmt"

	"larik/internal/database"
	"larik/internal/desktop/panes"
)

// Execute uses the agent-access path; the window checks authorization both
// before and after this I/O before returning its result.
func Execute(ctx context.Context, manager *database.Manager, profile string, req panes.DatabaseControlRequest) ([]database.Table, *database.Result, error) {
	switch req.Op {
	case "schema":
		tables, err := manager.Schema(ctx, profile, true)
		return tables, nil, err
	case "rows":
		result, err := manager.Rows(ctx, profile, req.Schema, req.Table, req.Offset, req.Limit, true)
		return nil, &result, err
	case "query":
		result, err := manager.Query(ctx, profile, req.SQL, req.Limit, true)
		return nil, &result, err
	default:
		return nil, nil, fmt.Errorf("unknown Database operation %q", req.Op)
	}
}
