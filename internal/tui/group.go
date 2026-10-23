package tui

import (
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"larik/internal/agent"
	"larik/internal/textutil"
)

// Consecutive read-only calls, such as a model reading a few files and
// searching a few patterns, print as one card ("Read 6 files · searched 3
// patterns") instead of one each. The calls collect in m.grouped until
// something else is printed, which prints them first (see handleEvent),
// so the transcript stays in order. A run of one keeps its own card.

// groupVerbs are the tools a run groups, with what the card says of them.
var groupVerbs = map[string]struct{ verb, noun string }{
	"read":       {"read", "file"},
	"grep":       {"searched", "pattern"},
	"glob":       {"listed", "glob"},
	"web_search": {"searched the web", "time"},
	"web_fetch":  {"fetched", "page"},
}

// groupable reports whether a finished call joins the run: a successful
// top-level call to a tool in groupVerbs whose card would show nothing
// more than a summary. Compact mode summarizes every call already, and
// verbose mode wants each one.
func (m *model) groupable(e agent.Event) bool {
	_, ok := groupVerbs[e.ToolName]
	return ok && m.appearance == "default" && e.Kind == agent.EvToolEnd && e.Agent == "" && !e.IsError &&
		e.Display == "" && diagnosticLines(e.Output) == "" && m.scriptOf(e.Agent, e.ToolID) == nil
}

// endsGroup reports whether e prints something, so the run must print
// before it.
func (m *model) endsGroup(e agent.Event) bool {
	switch e.Kind {
	case agent.EvToolStart, agent.EvToolProgress, agent.EvTextDelta, agent.EvThinkingDelta, agent.EvToolCallDelta, agent.EvUsage:
		return false
	case agent.EvToolEnd:
		// A script's calls print with the script.
		return !m.groupable(e) && m.scriptOf(e.Agent, e.ToolID) == nil
	case agent.EvAssistant:
		// A reply that only calls tools prints nothing.
		if e.Message == nil {
			return false
		}
		for _, b := range e.Message.Blocks {
			if strings.TrimSpace(b.Text) != "" {
				return true
			}
		}
		return false
	}
	return true
}

// flushGroup prints the run collected so far, if any.
func (m *model) flushGroup() tea.Cmd {
	calls := m.grouped
	m.grouped = nil
	switch len(calls) {
	case 0:
		return nil
	case 1:
		return m.println(m.renderToolCard(calls[0]))
	}
	return m.println(m.renderGroupCard(calls))
}

// groupSummary is "Read 6 files · searched 3 patterns", in the order the
// tools first ran. Files read count once however often they were read.
func groupSummary(calls []agent.Event) string {
	counts := map[string]int{}
	seen := map[string]bool{}
	var order []string
	for _, c := range calls {
		if _, ok := counts[c.ToolName]; !ok {
			order = append(order, c.ToolName)
		}
		if c.ToolName == "read" {
			var in struct{ Path string }
			_ = json.Unmarshal(c.Input, &in)
			if seen[in.Path] {
				continue
			}
			seen[in.Path] = true
		}
		counts[c.ToolName]++
	}
	parts := make([]string, len(order))
	for i, name := range order {
		v := groupVerbs[name]
		parts[i] = v.verb + " " + textutil.Plural(counts[name], v.noun)
	}
	s := strings.Join(parts, " · ")
	return strings.ToUpper(s[:1]) + s[1:]
}

// renderGroupCard is a run's card: its summary, and what each call was
// about on one line beneath.
func (m *model) renderGroupCard(calls []agent.Event) string {
	head := m.st.ok.Render("●") + " " + lipgloss.NewStyle().Bold(true).Render(groupSummary(calls))
	titles := make([]string, len(calls))
	for i, c := range calls {
		titles[i] = toolTitle(c.ToolName, c.Input, m.shortPaths)
	}
	width := 100
	if m.width > 0 {
		width = max(m.width-8, 20)
	}
	return head + "\n" + prefixLines(m.st.dim.Render(ansi.Truncate(strings.Join(titles, ", "), width, "…")), "  ⎿ ", "    ")
}

// groupLine is the live view's note of a run not yet printed.
func (m *model) groupLine() string {
	if len(m.grouped) == 0 {
		return ""
	}
	return m.st.ok.Render("✓ ") + m.st.dim.Render(groupSummary(m.grouped))
}
