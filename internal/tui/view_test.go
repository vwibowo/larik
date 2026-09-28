package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
)

func plain(s string) string { return ansi.Strip(s) }

func TestFooterDropsHintsWhenNarrow(t *testing.T) {
	m := testModel(t)
	m.stats = agent.UsageInfo{ContextWindow: 1000, ContextTokens: 310}
	m.agent.SetEffort(llm.EffortHigh)

	m.width = 120
	wide := plain(m.statusLine())
	for _, want := range []string{"default", "shift+tab", "◆ m ollama", "effort high", "▰▰▰▱", "31%"} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide footer lacks %q: %q", want, wide)
		}
	}

	m.width = 50
	narrow := plain(m.statusLine())
	if strings.Contains(narrow, "shift+tab") || strings.Contains(narrow, "▰") || strings.Contains(narrow, "\n") {
		t.Errorf("narrow footer should drop hints and the bar and stay on one line: %q", narrow)
	}
	if !strings.Contains(narrow, "31%") || lipgloss.Width(narrow) > 50 {
		t.Errorf("narrow footer should keep the percentage and fit: %q", narrow)
	}

	m.agent.Perms().SetMode(permission.ModeYolo)
	if !strings.Contains(plain(m.statusLine()), "⚠ yolo") {
		t.Error("yolo should show in the mode chip")
	}
}

func TestThinkingCollapsesToOneLine(t *testing.T) {
	m := testModel(t)
	msg := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockThinking, Text: "first I will look\nthen I will answer"},
		{Type: llm.BlockText, Text: "done"},
	}}
	out := plain(m.renderAssistant(msg, 14*time.Second))
	if !strings.Contains(out, "Thought for 14s") || strings.Contains(out, "first I will look") {
		t.Fatalf("collapsed: %q", out)
	}
	m.toggleThinking()
	out = plain(m.renderAssistant(msg, 14*time.Second))
	if !strings.Contains(out, "first I will look") {
		t.Fatalf("ctrl+o should show thinking in full: %q", out)
	}
}

func TestLiveViewStatusLine(t *testing.T) {
	m := testModel(t)
	m.running, m.turnStart = true, time.Now().Add(-75*time.Second)
	m.handleEvent(agent.Event{Kind: agent.EvThinkingDelta, Text: strings.Repeat("x", 800)})
	line := plain(m.liveView())
	for _, want := range []string{"Thinking…", "1m 15s", "~200 tokens", "ctrl+o to show thinking", "esc to interrupt"} {
		if !strings.Contains(line, want) {
			t.Errorf("status lacks %q: %q", want, line)
		}
	}
	if strings.Contains(line, "xxxx") {
		t.Error("collapsed thinking should not stream its text")
	}
	m.handleEvent(agent.Event{Kind: agent.EvTextDelta, Text: "Hello"})
	if m.thinkDur == 0 || !strings.Contains(plain(m.liveView()), "Responding…") {
		t.Errorf("text should end thinking: dur %v view %q", m.thinkDur, plain(m.liveView()))
	}
}

func TestComposerFillsTerminalWidth(t *testing.T) {
	m := testModel(t)
	for _, width := range []int{8, 30, 80, 120} {
		m.setWidth(width)
		box := m.st.box.Width(max(m.width, 7)).Render(m.input.View())
		for _, line := range strings.Split(box, "\n") {
			if got := lipgloss.Width(line); got != width {
				t.Errorf("terminal %d: composer line width %d: %q", width, got, plain(line))
			}
		}
	}
}

func TestReadCardBoundsPreview(t *testing.T) {
	m := testModel(t)
	m.setWidth(55)
	out := strings.Repeat("A", 200) + "\nsecond\nthird\nfourth\nfifth\n"
	e := agent.Event{ToolName: "read", Input: []byte(`{"path":"test.go"}`), Output: out}
	card := plain(m.renderToolCard(e))
	if !strings.Contains(card, "read 5 lines") || strings.Contains(card, "AAAA") {
		t.Fatalf("default read should be collapsed: %q", card)
	}
	m.verbose = true
	card = plain(m.renderToolCard(e))
	if !strings.Contains(card, "second") || !strings.Contains(card, "… +2 lines") || strings.Contains(card, "fourth") {
		t.Fatalf("verbose read should show only a short preview: %q", card)
	}
	for _, line := range strings.Split(card, "\n") {
		if lipgloss.Width(line) > 55 {
			t.Fatalf("preview overflows terminal: %q", line)
		}
	}
}

func TestParallelSubagentRowsTrackActivity(t *testing.T) {
	m := testModel(t)
	m.setWidth(90)
	m.height, m.running = 24, true
	first := []byte(`{"subagent_type":"explore","description":"find UI"}`)
	second := []byte(`{"subagent_type":"general-purpose","description":"write tests"}`)
	m.handleEvent(agent.Event{Kind: agent.EvToolStart, ToolID: "one", ToolName: "task", Input: first})
	m.handleEvent(agent.Event{Kind: agent.EvToolStart, ToolID: "two", ToolName: "task", Input: second})
	m.handleEvent(agent.Event{Kind: agent.EvToolStart, ToolID: "child", ToolName: "read", Agent: "explore: find UI", Input: []byte(`{"path":"view.go"}`)})
	view := plain(m.liveView())
	for _, want := range []string{"Subagents (2 running)", "explore: find UI", "general-purpose: write tests", "read(view.go)", "0 tools"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in %q", want, view)
		}
	}
	m.handleEvent(agent.Event{Kind: agent.EvToolEnd, ToolID: "child", ToolName: "read", Agent: "explore: find UI"})
	if !strings.Contains(plain(m.liveView()), "1 tool") {
		t.Fatalf("completed call not counted: %q", plain(m.liveView()))
	}
	m.handleEvent(agent.Event{Kind: agent.EvToolEnd, ToolID: "one", ToolName: "task", Input: first})
	if strings.Contains(plain(m.liveView()), "explore: find UI") || !strings.Contains(plain(m.liveView()), "Subagents (1 running)") {
		t.Fatalf("finished task not removed: %q", plain(m.liveView()))
	}
}

func TestBackgroundSubagentVisibleBetweenTurns(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	m.height = 24
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	id, err := m.agent.StartBackground("explore: inspect code", func(ctx context.Context, emit func(agent.Event)) (string, bool) {
		select {
		case <-stop:
		case <-ctx.Done():
		}
		return "done", false
	})
	if err != nil {
		t.Fatal(err)
	}
	view := plain(m.View().Content)
	for _, want := range []string{id, "explore: inspect code", "Background tasks running…"} {
		if !strings.Contains(view, want) {
			t.Errorf("missing %q in %q", want, view)
		}
	}
}

func TestShortPath(t *testing.T) {
	if got := shortPath("/a/b"); got != "/a/b" {
		t.Errorf("short path changed: %q", got)
	}
	long := "/private/tmp/claude-501/some-very-long-directory-name-here/another/project"
	if got := shortPath(long); got != "…/another/project" {
		t.Errorf("long path: %q", got)
	}
}

func TestShortPaths(t *testing.T) {
	m := testModel(t)
	cwd := m.opts.Config.Cwd
	home := homeDir()
	cases := []struct{ in, want string }{
		{cwd + "/b.go\n" + cwd + "/sub/a.txt", "b.go\nsub/a.txt"},
		{"Created " + cwd + "/hello.txt (1 line)", "Created hello.txt (1 line)"},
		{"in " + cwd, "in ."},
		{cwd + "-old/x.go", cwd + "-old/x.go"}, // a sibling whose name starts the same
		{"/etc/hosts", "/etc/hosts"},
		{home + "/Code/other/x.go", "~/Code/other/x.go"},
	}
	for _, c := range cases {
		if got := m.shortPaths(c.in); got != c.want {
			t.Errorf("shortPaths(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	card := plain(m.renderToolCard(agent.Event{ToolName: "grep", Input: []byte(`{"pattern":"TODO","path":"` + cwd + `/internal"}`), Output: cwd + "/internal/a.go:3: TODO"}))
	if strings.Contains(card, cwd) || !strings.Contains(card, "grep(TODO in internal)") || !strings.Contains(card, "internal/a.go:3: TODO") {
		t.Errorf("tool card should use short paths:\n%s", card)
	}
}

func TestResumedThinkingShowsSavedTime(t *testing.T) {
	m := testModel(t)
	msg := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockThinking, Text: "hmm", DurationMS: 14_200}, llm.TextBlock("hi")}}
	if out := plain(m.renderAssistant(msg, 0)); !strings.Contains(out, "Thought for 14s") {
		t.Fatalf("resumed history should show the saved time: %q", out)
	}
}
