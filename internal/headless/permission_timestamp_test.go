package headless

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"larik/internal/agent"
)

func TestJSONPermissionPreservesCreationTime(t *testing.T) {
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var out bytes.Buffer
	p := &printer{format: FormatJSON, enc: json.NewEncoder(&out)}
	p.handle(agent.Event{Kind: agent.EvPermission, RequestedAt: at, TaskID: "bg-1"})
	var got agent.Event
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !got.RequestedAt.Equal(at) || got.TaskID != "bg-1" {
		t.Fatalf("permission JSON: %s (%v)", out.String(), err)
	}
}
