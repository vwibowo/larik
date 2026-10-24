package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"larik/internal/agent"
	"larik/internal/textutil"
	"larik/internal/tools"
)

// A run_code script's calls arrive as tool events of their own, with ids
// that extend the script's ("c1" makes "c1.1", "c1.2", …), and all end
// before the script does. The transcript shows them inside the script's
// card rather than as cards of their own, which would come before it.

// scriptRun collects a running script's finished calls for its card.
type scriptRun struct {
	started time.Time
	label   string // what the script says it does (scriptLabel)
	calls   []agent.Event
}

func scriptKey(agentLabel, id string) string { return agentLabel + "\x00" + id }

// startScript tracks a run_code call that began.
func (m *model) startScript(e agent.Event) {
	if m.scripts == nil {
		m.scripts = make(map[string]*scriptRun)
	}
	var in struct{ Code string }
	_ = json.Unmarshal(e.Input, &in)
	m.scripts[scriptKey(e.Agent, e.ToolID)] = &scriptRun{started: time.Now(), label: scriptLabel(in.Code)}
}

// scriptOf returns the running script that made call id, if one did.
func (m *model) scriptOf(agentLabel, id string) *scriptRun {
	i := strings.LastIndexByte(id, '.')
	if i <= 0 {
		return nil
	}
	return m.scripts[scriptKey(agentLabel, id[:i])]
}

// endScript stops tracking a script and returns what it collected (nil
// when its start was missed, such as after a reset).
func (m *model) endScript(e agent.Event) *scriptRun {
	key := scriptKey(e.Agent, e.ToolID)
	s := m.scripts[key]
	delete(m.scripts, key)
	return s
}

// scriptLabel names a script by its first line, or by the comment it
// opens with, which models usually write to say what it does.
func scriptLabel(code string) string {
	for _, line := range strings.Split(code, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "//"):
			line = strings.TrimSpace(strings.TrimLeft(line, "/"))
		case strings.HasPrefix(line, "/*"):
			line = strings.TrimSpace(strings.TrimSuffix(strings.TrimLeft(line, "/*"), "*/"))
		}
		if line != "" {
			return line
		}
	}
	return ""
}

// scriptResult splits run_code's output from the line counting its tool
// calls, which the model needs and the card says in its title instead.
func scriptResult(out string) (body string, calls int, ok bool) {
	out = strings.TrimSpace(out)
	last := out
	if i := strings.LastIndexByte(out, '\n'); i >= 0 {
		body, last = out[:i], out[i+1:]
	}
	if !strings.HasPrefix(last, "["+tools.CodeToolName+": ") || !strings.HasSuffix(last, "]") {
		return out, 0, false
	}
	if _, err := fmt.Sscanf(last, "["+tools.CodeToolName+": %d", &calls); err != nil {
		return out, 0, false
	}
	return strings.TrimSpace(body), calls, true
}

// scriptMeta is the card's "· 3 calls · 0.4s".
func scriptMeta(calls int, took time.Duration) string {
	meta := " · " + textutil.Plural(calls, "call")
	switch {
	case took < 100*time.Millisecond: // not worth saying
	case took < 10*time.Second:
		meta += fmt.Sprintf(" · %.1fs", took.Seconds())
	default:
		meta += " · " + elapsed(took)
	}
	return meta
}

// renderScriptCard is a finished script: its title, the start of its
// code, the calls it made and what it printed. s is nil when the calls
// weren't collected; the card then counts them from the output.
func (m *model) renderScriptCard(e agent.Event, s *scriptRun) string {
	bullet := m.toolMark(e.IsError)
	out, calls, _ := scriptResult(m.shortPaths(e.Output))
	var took time.Duration
	if s != nil {
		calls, took = len(s.calls), time.Since(s.started)
	}
	head := bullet + " " + m.toolHead(e.ToolName, e.Input) +
		m.st.dim.Render(scriptMeta(calls, took))
	if e.Agent != "" { // a subagent's script stays one line, like its other calls
		return prefixLines(head, "  ↳ ", "    ") + m.st.dim.Render("  ("+e.Agent+")")
	}

	lines := []string{head}
	for _, l := range m.scriptPreview(e.Input) {
		lines = append(lines, "  "+m.st.dim.Render("│ "+l))
	}
	if s != nil {
		lines = append(lines, m.scriptCallLines(s.calls)...)
	}
	if out != "" {
		style := m.st.dim
		if e.IsError {
			style = m.st.err
		}
		lines = append(lines, prefixLines(style.Render(truncateLines(out, m.lines(6))), "  ⎿ ", "    "))
	}
	return strings.Join(lines, "\n")
}

// scriptPreview is the start of a script's code: a few lines, all of
// them in verbose mode.
func (m *model) scriptPreview(input []byte) []string {
	var in struct{ Code string }
	_ = json.Unmarshal(input, &in)
	var code []string
	for _, l := range strings.Split(strings.TrimSpace(in.Code), "\n") {
		if strings.TrimSpace(l) != "" {
			code = append(code, strings.TrimRight(strings.ReplaceAll(l, "\t", "  "), " "))
		}
	}
	// A comment the title already shows isn't repeated.
	if len(code) > 1 && strings.HasPrefix(strings.TrimSpace(code[0]), "/") {
		code = code[1:]
	}
	limit := 3
	if m.verbose {
		limit = m.lines(30)
	}
	width := 100
	if m.width > 0 {
		width = max(min(m.width-8, 120), 20)
	}
	var out []string
	for i, l := range code {
		if i == limit {
			out = append(out, "… +"+textutil.Plural(len(code)-limit, "line"))
			break
		}
		out = append(out, ansi.Truncate(l, width, "…"))
	}
	return out
}

// scriptCallLines lists a script's calls under its code. A call shows its
// body only when it says something the title doesn't: an edit's diff, an
// error or diagnostics (every body in verbose mode). Runs of calls to the
// same tool without a body collapse into one line.
func (m *model) scriptCallLines(calls []agent.Event) []string {
	type entry struct {
		e    agent.Event
		body string
		more int
	}
	var entries []*entry
	for _, c := range calls {
		c.Output, c.Display = m.shortPaths(c.Output), m.shortPaths(c.Display)
		var body string
		if m.verbose || c.IsError || c.Display != "" || c.ToolName == "write" || diagnosticLines(c.Output) != "" {
			body = m.toolBody(c)
		}
		if n := len(entries); body == "" && n > 0 && entries[n-1].body == "" && entries[n-1].e.ToolName == c.ToolName && !entries[n-1].e.IsError {
			entries[n-1].more++
			continue
		}
		entries = append(entries, &entry{e: c, body: body})
	}
	limit := len(entries)
	if !m.verbose {
		limit = min(limit, m.lines(8))
	}
	var lines []string
	for i, en := range entries {
		if i == limit {
			hidden := 0
			for _, rest := range entries[i:] {
				hidden += 1 + rest.more
			}
			lines = append(lines, "  "+m.st.dim.Render("├ … +"+textutil.Plural(hidden, "more call")))
			break
		}
		title := toolTitle(en.e.ToolName, en.e.Input, m.shortPaths)
		if en.e.IsError {
			title = m.st.err.Render(title)
		}
		if en.more > 0 {
			title += m.st.dim.Render(fmt.Sprintf(" +%d more", en.more))
		}
		lines = append(lines, "  "+m.st.dim.Render("├ ")+title)
		if en.body != "" {
			lines = append(lines, prefixLines(en.body, "  "+m.st.dim.Render("│   ")))
		}
	}
	return lines
}
