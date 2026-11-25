package databasepane

import "encoding/json"

// Snapshot keeps view preferences without retaining credentials.
type Snapshot struct {
	View json.RawMessage `json:"view"`
}
