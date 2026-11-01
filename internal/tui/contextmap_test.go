package tui

import (
	"encoding/json"
	"strings"
	"testing"

	"larik/internal/agent"
)

func TestLayoutMapTilesTheArea(t *testing.T) {
	weights := []int{50, 20, 10, 8, 6, 3, 2, 1}
	rects := make([]mapRect, len(weights))
	layoutMap(weights, rects, mapRect{0, 0, 60, 14})
	seen := map[[2]int]int{}
	for _, r := range rects {
		if r.w < 1 || r.h < 1 {
			t.Fatalf("an empty box: %+v", rects)
		}
		for x := r.x; x < r.x+r.w; x++ {
			for y := r.y; y < r.y+r.h; y++ {
				seen[[2]int{x, y}]++
			}
		}
	}
	if len(seen) != 60*14 {
		t.Fatalf("%d cells covered of %d", len(seen), 60*14)
	}
	for c, n := range seen {
		if n != 1 {
			t.Fatalf("cell %v covered %d times", c, n)
		}
	}
	first := rects[0].w * rects[0].h
	if first < 60*14*4/10 || first > 60*14*6/10 {
		t.Fatalf("half the weight got %d of %d cells", first, 60*14)
	}
}

func contextMapSample() agent.ContextMap {
	return agent.ContextMap{
		Parts: []agent.ContextPart{{Name: "read", Weight: 6000}, {Name: "replies", Weight: 3000}, {Name: "bash", Weight: 950}, {Name: "grep", Weight: 50}},
		Total: 10000, Tokens: 50000, Window: 200000,
		Largest: []agent.ContextItem{{Tool: "read", Input: json.RawMessage(`{"path":"big.go"}`), Weight: 4000}},
	}
}

func TestContextMapShowsSharesNeverEstimatedTokens(t *testing.T) {
	m := testModel(t)
	got := plain(m.renderContextMap(contextMapSample(), 80))
	for _, want := range []string{"25% of 200k", "read", "60%", "replies", "30%", "other: grep", "40%", "big.go"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n > 80 {
			t.Errorf("line of %d cells: %q", n, line)
		}
	}
	if strings.Contains(got, "6000") || strings.Contains(got, "6.0k") {
		t.Errorf("an estimate shown as tokens:\n%s", got)
	}
}

func TestContextMapFitsNarrowWindowsAndUnmeasuredContext(t *testing.T) {
	m := testModel(t)
	c := contextMapSample()
	c.Tokens = 0
	got := plain(m.renderContextMap(c, 20))
	if !strings.Contains(got, "not measured") || !strings.Contains(got, " 60%  read") {
		t.Fatalf("narrow and unmeasured:\n%s", got)
	}
}
