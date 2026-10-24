package server

import (
	"encoding/json"
	"testing"
	"time"

	"larik/internal/agent"
)

func TestPermissionReplayPreservesCreationTime(t *testing.T) {
	at := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	b := newBus()
	b.publish(Event{Session: "s", RequestID: "perm-1", Event: agent.Event{Kind: agent.EvPermission, RequestedAt: at}})
	replay, ch, _ := b.subscribe(0)
	defer b.unsubscribe(ch)
	if len(replay) != 1 {
		t.Fatalf("replay = %v", replay)
	}
	data, err := json.Marshal(replay[0])
	if err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal(data, &got); err != nil || !got.RequestedAt.Equal(at) || got.RequestID != "perm-1" {
		t.Fatalf("replay JSON = %s (%v)", data, err)
	}
}
