package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"larik/internal/agent"
)

var timelineStart = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

func at(seconds int) time.Time { return timelineStart.Add(time.Duration(seconds) * time.Second) }

func sampleTimeline() *timeline {
	var t timeline
	t.note(agent.Event{Kind: agent.EvUsage, Usage: &agent.UsageInfo{RequestMS: 10000}}, at(10))
	t.note(agent.Event{Kind: agent.EvToolStart, ToolID: "1", ToolName: "bash", Input: json.RawMessage(`{"command":"go test ./..."}`)}, at(10))
	t.note(agent.Event{Kind: agent.EvToolStart, Agent: "explore: auth", ToolID: "1", ToolName: "read"}, at(12))
	t.note(agent.Event{Kind: agent.EvToolEnd, Agent: "explore: auth", ToolID: "1", ToolName: "read"}, at(14))
	t.note(agent.Event{Kind: agent.EvToolEnd, ToolID: "1", ToolName: "bash"}, at(70))
	t.note(agent.Event{Kind: agent.EvUsage, Agent: "explore: auth", Usage: &agent.UsageInfo{CostUSD: 1}}, at(71))
	return &t
}

func TestTimelineMatchesEndsToTheirCalls(t *testing.T) {
	got := sampleTimeline().spans
	if len(got) != 3 {
		t.Fatalf("%d spans: %+v", len(got), got)
	}
	if !got[0].model || got[0].start != at(0) || got[0].end != at(10) {
		t.Errorf("a request reaches back by its duration: %+v", got[0])
	}
	if got[1].name != "bash" || got[1].end != at(70) || got[2].end != at(14) {
		t.Errorf("the same id in another agent is another call: %+v", got[1:])
	}
}

func TestActiveTimeCountsOverlapsOnce(t *testing.T) {
	spans := []timelineSpan{{start: at(0), end: at(10)}, {start: at(5), end: at(8)}, {start: at(9), end: at(20)}, {start: at(100), end: at(101)}}
	if got := activeTime(spans); got != 21*time.Second {
		t.Fatalf("active %v", got)
	}
}

func TestTimelineAxisCutsLongPausesShort(t *testing.T) {
	spans := []timelineSpan{{start: at(0), end: at(30)}, {start: at(36000), end: at(36030)}}
	ax := newTimelineAxis(spans)
	if want := 30*time.Second + timelineMaxGap + 30*time.Second; ax.total != want {
		t.Fatalf("axis of %v, want %v", ax.total, want)
	}
	if got := ax.pos(at(36000)); got != 90*time.Second {
		t.Fatalf("second burst at %v", got)
	}
	got := ax.bars(spans[:1], 12)
	if strings.Count(got, "█") < 3 || strings.Count(got, "█") > 4 {
		t.Fatalf("a quarter of the axis: %q", got)
	}
}

func TestTimelineShowsRowsSlowestCallsAndCost(t *testing.T) {
	m := testModel(t)
	got := plain(m.renderTimeline(sampleTimeline().spans, agent.UsageInfo{CostUSD: 1.5, DelegatedCostUSD: 0.5}, 90))
	for _, want := range []string{"1m 10s of work", "model", "10s", "tools", "1m 00s", "explore: auth", "go test", "main $1.00, subagents $0.50"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if n := len([]rune(line)); n > 90 {
			t.Errorf("line of %d cells: %q", n, line)
		}
	}
	if narrow := plain(m.renderTimeline(sampleTimeline().spans, agent.UsageInfo{}, 30)); strings.Contains(narrow, "█") {
		t.Errorf("no room for bars:\n%s", narrow)
	}
}

func TestTimelineGroupsManySubagents(t *testing.T) {
	var spans []timelineSpan
	for i := range timelineMaxRows + 4 {
		spans = append(spans, timelineSpan{agent: string(rune('a' + i)), start: at(i), end: at(i + 1)})
	}
	rows := timelineRows(spans)
	if len(rows) != timelineMaxRows || rows[len(rows)-1].label != "5 more" {
		t.Fatalf("%d rows, last %q", len(rows), rows[len(rows)-1].label)
	}
}

func TestRenderTimelineSaysWhenItIsEmptyAndAfterAPause(t *testing.T) {
	m := testModel(t)
	if got := plain(m.renderTimeline(nil, agent.UsageInfo{}, 80)); !strings.Contains(got, "Nothing has run") {
		t.Fatalf("empty: %q", got)
	}
	spans := []timelineSpan{{start: at(7200), end: at(7210)}, {start: at(0), end: at(10)}}
	if got := plain(m.renderTimeline(spans, agent.UsageInfo{}, 80)); !strings.Contains(got, "20s of work over 2h 00m") {
		t.Fatalf("pause: %q", got)
	}
}
