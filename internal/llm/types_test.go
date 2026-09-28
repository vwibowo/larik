package llm

import (
	"strings"
	"testing"
)

func TestNewCallIDIsUnique(t *testing.T) {
	a, b := NewCallID("call_"), NewCallID("call_")
	if a == b || !strings.HasPrefix(a, "call_") || len(a) != len("call_")+16 {
		t.Fatalf("ids %q and %q", a, b)
	}
}
