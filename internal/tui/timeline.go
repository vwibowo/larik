package tui

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"larik/internal/agent"
)

// /timeline shows when the model and tools ran since Larik started, read off
// the event stream, so it needs no debug mode. It knows only this run: a
// session file doesn't keep timings.

const (
	timelineLabelCols = 18
	timelineValueCols = 9
	timelineMinBars   = 16
	timelineMaxBars   = 72
	// timelineMaxRows bounds the subagent rows; the rest share one.
	timelineMaxRows = 8
	// timelineSlowest is how many of the longest tool calls are listed.
	timelineSlowest = 5
	// timelineMaxGap is the longest pause the axis draws at full length, so
	// a session left overnight still shows its work.
	timelineMaxGap = time.Minute
)

type timelineSpan struct {
	agent      string
	id, name   string
	input      json.RawMessage
	start, end time.Time // end is zero while a call runs
	model      bool
}

// timeline is what has run so far.
type timeline struct{ spans []timelineSpan }

// note records what an event says about time. now is when it arrived.
func (t *timeline) note(e agent.Event, now time.Time) {
	switch e.Kind {
	case agent.EvToolStart:
		t.spans = append(t.spans, timelineSpan{agent: e.Agent, id: e.ToolID, name: e.ToolName, input: e.Input, start: now})
	case agent.EvToolEnd:
		for i := len(t.spans) - 1; i >= 0; i-- {
			s := &t.spans[i]
			if s.id == e.ToolID && s.agent == e.Agent && !s.model && s.end.IsZero() {
				s.end = now
				return
			}
		}
	case agent.EvUsage:
		// A subagent's forwarded usage carries totals and no request time.
		if e.Usage != nil && e.Usage.RequestMS > 0 {
			ms := time.Duration(e.Usage.RequestMS) * time.Millisecond
			t.spans = append(t.spans, timelineSpan{start: now.Add(-ms), end: now, model: true})
		}
	}
}

// timelineRow is one line of the chart: its spans and their time on the clock.
type timelineRow struct {
	label string
	spans []timelineSpan
}

// activeTime is how long at least one of spans ran; calls side by side count
// once.
func activeTime(spans []timelineSpan) time.Duration {
	sorted := slices.Clone(spans)
	slices.SortFunc(sorted, func(a, b timelineSpan) int { return a.start.Compare(b.start) })
	var total time.Duration
	var end time.Time
	for _, s := range sorted {
		if s.start.After(end) {
			end = s.start
		}
		if s.end.After(end) {
			total += s.end.Sub(end)
			end = s.end
		}
	}
	return total
}

// timelineAxis places instants on a line where long pauses are cut short.
type timelineAxis struct {
	marks []timelineMark
	total time.Duration
}

// timelineMark says that from at on, the axis stands at pos.
type timelineMark struct {
	at  time.Time
	pos time.Duration
}

func newTimelineAxis(spans []timelineSpan) timelineAxis {
	sorted := slices.Clone(spans)
	slices.SortFunc(sorted, func(a, b timelineSpan) int { return a.start.Compare(b.start) })
	var ax timelineAxis
	end := sorted[0].start
	ax.marks = []timelineMark{{at: end}}
	for _, s := range sorted {
		if gap := s.start.Sub(end); gap > timelineMaxGap {
			ax.total += s.start.Sub(ax.marks[len(ax.marks)-1].at) - gap + timelineMaxGap
			ax.marks = append(ax.marks, timelineMark{s.start, ax.total})
		}
		if s.end.After(end) {
			end = s.end
		}
	}
	last := ax.marks[len(ax.marks)-1]
	ax.total = last.pos + end.Sub(last.at)
	return ax
}

// pos is t's place on the axis.
func (ax timelineAxis) pos(t time.Time) time.Duration {
	i, ok := slices.BinarySearchFunc(ax.marks, t, func(m timelineMark, t time.Time) int { return m.at.Compare(t) })
	if !ok {
		i = max(i-1, 0)
	}
	return ax.marks[i].pos + t.Sub(ax.marks[i].at)
}

// bars draws spans on cols cells; even the briefest call fills one.
func (ax timelineAxis) bars(spans []timelineSpan, cols int) string {
	cells := make([]bool, cols)
	scale := max(ax.total, time.Millisecond)
	cell := func(t time.Time) int { return min(int(ax.pos(t)*time.Duration(cols)/scale), cols-1) }
	for _, s := range spans {
		for i := cell(s.start); i <= cell(s.end); i++ {
			cells[i] = true
		}
	}
	var b strings.Builder
	for _, on := range cells {
		if on {
			b.WriteRune('█')
		} else {
			b.WriteRune(' ')
		}
	}
	return b.String()
}

func timelineDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return "<1s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
}

// timelineRows groups spans: the model, the main agent's tools, then each
// subagent, busiest first.
func timelineRows(spans []timelineSpan) []timelineRow {
	var model, main []timelineSpan
	byAgent := map[string][]timelineSpan{}
	for _, s := range spans {
		switch {
		case s.model:
			model = append(model, s)
		case s.agent == "":
			main = append(main, s)
		default:
			byAgent[s.agent] = append(byAgent[s.agent], s)
		}
	}
	var rows []timelineRow
	for _, r := range []timelineRow{{"model", model}, {"tools", main}} {
		if len(r.spans) > 0 {
			rows = append(rows, r)
		}
	}
	var agents []timelineRow
	for label, s := range byAgent {
		agents = append(agents, timelineRow{label, s})
	}
	slices.SortFunc(agents, func(a, b timelineRow) int {
		return cmp.Or(cmp.Compare(activeTime(b.spans), activeTime(a.spans)), cmp.Compare(a.label, b.label))
	})
	if len(agents) > timelineMaxRows {
		rest := timelineRow{label: fmt.Sprintf("%d more", len(agents)-timelineMaxRows+1)}
		for _, r := range agents[timelineMaxRows-1:] {
			rest.spans = append(rest.spans, r.spans...)
		}
		agents = append(agents[:timelineMaxRows-1], rest)
	}
	return append(rows, agents...)
}

func (m *model) timelineCommand() tea.Cmd {
	now := time.Now()
	spans := slices.Clone(m.timeline.spans)
	for i := range spans {
		if spans[i].end.IsZero() {
			spans[i].end = now
		}
	}
	stats := m.agent.Stats()
	return m.printlnRendered(256, func(m *model, width int) string {
		return m.renderTimeline(spans, stats, width)
	})
}

func (m *model) renderTimeline(spans []timelineSpan, stats agent.UsageInfo, width int) string {
	if len(spans) == 0 {
		return m.st.dim.Render("  Nothing has run since Larik started.")
	}
	ax := newTimelineAxis(spans)
	bars := min(width-timelineLabelCols-timelineValueCols-4, timelineMaxBars)
	styles := []func(...string) string{m.st.accent.Render, m.st.ok.Render, m.st.spark.Render, m.st.warn.Render}
	var b []string
	head := "  " + timelineDuration(activeTime(spans)) + " of work"
	if len(ax.marks) > 1 {
		head += m.st.dim.Render(" over " + timelineDuration(clockTime(spans)) + ", long pauses cut short")
	}
	b = append(b, head)
	for i, r := range timelineRows(spans) {
		label := fmt.Sprintf("  %-*s", timelineLabelCols, ansi.Truncate(r.label, timelineLabelCols-1, "…"))
		value := fmt.Sprintf("%*s", timelineValueCols, timelineDuration(activeTime(r.spans)))
		line := label + value
		if bars >= timelineMinBars {
			line = label + styles[i%len(styles)](ax.bars(r.spans, bars)) + " " + value
		}
		b = append(b, line)
	}
	if slowest := m.slowestCalls(spans, width); len(slowest) > 0 {
		b = append(b, "", "  Slowest calls")
		b = append(b, slowest...)
	}
	if stats.CostUSD > 0 {
		cost := fmt.Sprintf("Cost $%.2f", stats.CostUSD)
		if stats.DelegatedCostUSD > 0 {
			cost += fmt.Sprintf(": main $%.2f, subagents $%.2f", max(stats.CostUSD-stats.DelegatedCostUSD, 0), stats.DelegatedCostUSD)
		}
		b = append(b, "", "  "+cost)
	}
	return strings.Join(b, "\n")
}

// clockTime is how long passed from the first start to the last end.
func clockTime(spans []timelineSpan) time.Duration {
	first, last := spans[0].start, spans[0].end
	for _, s := range spans {
		if s.start.Before(first) {
			first = s.start
		}
		if s.end.After(last) {
			last = s.end
		}
	}
	return last.Sub(first)
}

// slowestCalls lists the longest calls. A task's time is its subagent's row.
func (m *model) slowestCalls(spans []timelineSpan, width int) []string {
	var calls []timelineSpan
	for _, s := range spans {
		if !s.model && s.name != "task" {
			calls = append(calls, s)
		}
	}
	slices.SortStableFunc(calls, func(a, b timelineSpan) int {
		return cmp.Compare(b.end.Sub(b.start), a.end.Sub(a.start))
	})
	var out []string
	for _, s := range calls[:min(len(calls), timelineSlowest)] {
		line := fmt.Sprintf("  %7s  %s", timelineDuration(s.end.Sub(s.start)), toolTitle(s.name, s.input, m.shortPaths))
		out = append(out, ansi.Truncate(line, max(width, 1), "…"))
	}
	return out
}
