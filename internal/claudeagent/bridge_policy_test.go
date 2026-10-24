package claudeagent

import (
	"context"
	"larik/internal/llm"
	"testing"
)

func TestBridgeRefusesSharedTempFallback(t *testing.T) {
	for _, dir := range []string{"", "relative/runtime"} {
		bridge, err := startBridge(context.Background(), []llm.ToolSpec{{Name: "read", Schema: []byte(`{"type":"object"}`)}}, nil, make(chan llm.AgentRuntimeEvent, 1), &callMatcher{}, dir)
		if err == nil {
			bridge.Close(context.Background())
			t.Fatalf("insecure bridge directory accepted: %q", dir)
		}
	}
}
