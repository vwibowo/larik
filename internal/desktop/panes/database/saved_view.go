package databasepane

import "larik/internal/desktop/panes"

type SavedView = panes.DatabaseSavedView

const (
	DataResult  = panes.DatabaseDataResult
	QueryResult = panes.DatabaseQueryResult
	AgentResult = panes.DatabaseAgentResult
)

func SaveView(d *State) SavedView       { return panes.SaveDatabaseView(d) }
func RestoreView(d *State, v SavedView) { panes.RestoreDatabaseView(d, v) }
