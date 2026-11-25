package databasepane

import "larik/internal/desktop/panes"

type State = panes.DatabaseState
type ResultView = panes.DatabaseResultView

func NewState() *State { return panes.NewDatabaseState() }
