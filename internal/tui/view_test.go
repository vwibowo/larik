package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"larik/internal/agent"
	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

func plain(s string) string { return ansi.Strip(s) }

func TestSessionInfoSidebarTogglePersistsWhileTypingAndResponding(t *testing.T) {
	m := testModel(t)
	m.setWidth(120)
	m.height = 30
	m.Update(outputMsg("conversation remains visible"))
	panel := plain(m.sessionInfoView())
	for _, want := range []string{"Session", "MCP", "Skills", "LSP"} {
		if !strings.Contains(panel, want) {
			t.Errorf("session info panel lacks %q: %q", want, panel)
		}
	}

	m.input.SetValue("draft stays intact")
	m.Update(press(tea.KeyF2))
	if !m.showInfo || m.input.Value() != "draft stays intact" {
		t.Fatalf("F2 should show the sidebar without changing the draft: showInfo=%v input=%q", m.showInfo, m.input.Value())
	}
	view := plain(m.View().Content)
	if !strings.Contains(view, "F2 to hide") || !strings.Contains(view, "conversation remains visible") || !strings.Contains(view, "draft stays intact") {
		t.Fatalf("sidebar, conversation, and composer should all remain visible: %q", view)
	}

	m.running = true
	m.stream.WriteString("streaming response")
	if view = plain(m.View().Content); !strings.Contains(view, "F2 to hide") || !strings.Contains(view, "streaming response") {
		t.Fatalf("sidebar should remain visible during a response: %q", view)
	}
	m.Update(press(tea.KeyF2))
	if m.showInfo {
		t.Fatal("F2 should hide the sidebar during a response")
	}
}

func TestSessionSidebarShowsOnlyActiveItemsAndFitsItsColumn(t *testing.T) {
	m := testModel(t)
	m.usedSkills = map[string]bool{"graphify": true}
	m.todos = []tools.Todo{
		{Content: "Make F2 and /info toggle a persistent sidebar with a long description", Status: tools.TodoCompleted},
		{Content: "Run formatting and focused validation", Status: tools.TodoInProgress},
	}
	for _, width := range []int{100, 160} {
		sidebarWidth := min(max(width/3, 30), 44)
		out := m.sessionSidebar(sidebarWidth, 24)
		lines := strings.Split(out, "\n")
		if len(lines) != 24 {
			t.Errorf("width %d: sidebar has %d rows, want 24", width, len(lines))
		}
		for _, l := range lines {
			if w := lipgloss.Width(l); w != sidebarWidth-1 {
				t.Errorf("width %d: row is %d columns, want %d: %q", width, w, sidebarWidth-1, plain(l))
			}
		}
		text := plain(out)
		for _, want := range []string{"Tasks", "MCP", "LSP", "Skills", "/graphify", "F2 to hide"} {
			if !strings.Contains(text, want) {
				t.Errorf("width %d: sidebar lacks %q: %s", width, want, text)
			}
		}
		for _, unwanted := range []string{"Permission mode", "Model:", "available"} {
			if strings.Contains(text, unwanted) {
				t.Errorf("width %d: sidebar still shows %q: %s", width, unwanted, text)
			}
		}
	}
}

func TestInfoCommandTogglesSidebar(t *testing.T) {
	m := testModel(t)
	m.command("/info")
	if !m.showInfo {
		t.Fatal("/info should show session info")
	}
	m.command("/info")
	if m.showInfo {
		t.Fatal("/info should hide session info")
	}
}

func TestFooterDropsHintsWhenNarrow(t *testing.T) {
	m := testModel(t)
	m.stats = agent.UsageInfo{ContextWindow: 1000, ContextTokens: 310}
	m.agent.SetEffort(llm.EffortHigh)

	m.width = 120
	wide := plain(m.statusLine())
	for _, want := range []string{"default", "shift+tab", "◆ m ollama", "effort high", "▰▰▰▱", "31%", "310/1k"} {
		if !strings.Contains(wide, want) {
			t.Errorf("wide footer lacks %q: %q", want, wide)
		}
	}

	m.width = 50
	narrow := plain(m.statusLine())
	if strings.Contains(narrow, "shift+tab") || strings.Contains(narrow, "▰") || strings.Contains(narrow, "310/1k") || strings.Contains(narrow, "\n") {
		t.Errorf("narrow footer should drop hints, the token counts and the bar and stay on one line: %q", narrow)
	}
	if !strings.Contains(narrow, "31%") || lipgloss.Width(narrow) > 50 {
		t.Errorf("narrow footer should keep the percentage and fit: %q", narrow)
	}

	// The token counts go before the bar, which goes before the provider.
	m.width = 74
	for _, want := range []string{"▰▰▰▱", "31%", "ollama"} {
		if got := plain(m.statusLine()); !strings.Contains(got, want) {
			t.Errorf("footer at 74 columns lacks %q: %q", want, got)
		}
	}
	if got := plain(m.statusLine()); strings.Contains(got, "310/1k") {
		t.Errorf("footer at 74 columns should have dropped the token counts: %q", got)
	}

	m.agent.Perms().SetMode(permission.ModeYolo)
	if !strings.Contains(plain(m.statusLine()), "⚠ yolo") {
		t.Error("yolo should show in the mode chip")
	}
}

// The window is known from the catalog or a probe before the first
// response, so the footer shows it right away.
func TestFooterShowsContextWindowBeforeAnyResponse(t *testing.T) {
	m := testModel(t)
	m.width = 120
	m.stats = agent.UsageInfo{ContextWindow: 200_000}
	got := plain(m.statusLine())
	for _, want := range []string{"ctx", "0%", "0/200k"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer lacks %q before the first response: %q", want, got)
		}
	}
	m.stats = agent.UsageInfo{ContextWindow: 1_000_000, ContextTokens: 62_300}
	if got := plain(m.statusLine()); !strings.Contains(got, "62k/1M") {
		t.Errorf("footer should read used tokens over the window: %q", got)
	}
}

// Your own messages carry a gutter on every line, wrapped ones included, so
// they are told apart from the model's replies.
func TestUserMessageGutterOnEveryLine(t *testing.T) {
	m := testModel(t)
	m.setWidth(40)
	m.convWidth = 40
	got := plain(m.renderUserMessage("this prompt is long enough that it has to wrap over several rows\nand it has a hard newline too"))
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("expected a wrapped message, got %q", got)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "▌ ") {
			t.Errorf("line without a gutter: %q in %q", l, got)
		}
		if lipgloss.Width(l) > 40 {
			t.Errorf("line overflows the conversation: %q", l)
		}
	}
}

func TestAgentsSectionShowsRunningModelsThenRouting(t *testing.T) {
	m := testModel(t)
	m.setWidth(120)
	m.height = 30

	// Idle: the routing a delegated task would take.
	m.opts.Config.Roles = map[string]string{"worker": "ollama/cheap"}
	m.opts.Config.RoleOptions = map[string]config.RoleOption{"worker": {Isolation: "worktree"}}
	idle := plain(m.sessionSidebar(44, 24))
	for _, want := range []string{"Agents", "idle", "worker", "ollama/cheap", "worktree"} {
		if !strings.Contains(idle, want) {
			t.Errorf("idle sidebar lacks %q: %q", want, idle)
		}
	}

	// Running: one row per subagent, naming the model it runs on.
	m.handleEvent(agent.Event{Kind: agent.EvToolStart, ToolID: "one", ToolName: "task",
		Input: []byte(`{"subagent_type":"explore","description":"find UI"}`)})
	m.handleEvent(agent.Event{Kind: agent.EvToolStart, ToolID: "child", ToolName: "read",
		Agent: "explore: find UI · haiku-4-5", Model: "haiku-4-5", Input: []byte(`{"path":"view.go"}`)})
	busy := plain(m.sessionSidebar(44, 24))
	for _, want := range []string{"1 running", "explore: find UI", "haiku-4-5"} {
		if !strings.Contains(busy, want) {
			t.Errorf("busy sidebar lacks %q: %q", want, busy)
		}
	}
	if strings.Contains(busy, "ollama/cheap") {
		t.Errorf("running subagents should replace the routing list: %q", busy)
	}

	// A role that inherits runs on the main model, which is what to show.
	m.taskModels = nil
	if got := plain(m.sessionSidebar(44, 24)); !strings.Contains(got, "explore: find UI") || !strings.Contains(got, " m ") {
		t.Errorf("an inherited role should show the main model: %q", got)
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
	for _, width := range []int{8, 30, 80, 120, 600} {
		m.setWidth(width)
		box := m.st.box.Width(max(m.width, 7)).Render(m.input.View())
		for _, line := range strings.Split(box, "\n") {
			if got := lipgloss.Width(line); got != width {
				t.Errorf("terminal %d: composer line width %d: %q", width, got, plain(line))
			}
		}
	}
}

func TestComposerStaysAtBottom(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	for _, height := range []int{14, 24} {
		m.height = height
		checkPinnedView(t, m)
	}
	m.input.SetValue("first\nsecond\nthird")
	checkPinnedView(t, m)
	m.running = true
	m.stream.WriteString(strings.Repeat("output line\n", 40))
	checkPinnedView(t, m)
	m.sessionPick = &picker{items: []pickItem{{label: "a session", value: "a"}}}
	checkPinnedView(t, m)
}

func TestPickersSitAboveComposer(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	m.height = 30
	m.Update(outputMsg("visible conversation"))
	m.palette = &picker{items: []pickItem{{label: "/model", detail: "pick a model"}}}
	assertPanelAtBottom(t, m, m.paletteView())
	m.palette = nil
	m.mpick = &modelPicker{list: picker{}}
	for i := range 30 {
		m.mpick.list.items = append(m.mpick.list.items, pickItem{label: fmt.Sprintf("ollama/model-%d", i), value: pickModel{provider: "ollama", model: "m"}})
	}
	for _, height := range []int{30, 16} {
		m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
		assertPanelAtBottom(t, m, m.modelPickerView())
		if m.view.Height() < 3 {
			t.Fatalf("model picker left only %d conversation rows", m.view.Height())
		}
	}
}

func TestInSessionPanelsShareBottomLayout(t *testing.T) {
	openers := []struct {
		name string
		open func(*model)
		want string
	}{
		{"settings", func(m *model) { m.openSettings("") }, "Settings"},
		{"providers", func(m *model) { m.openProviders() }, "Providers"},
		{"connect", func(m *model) { m.openWizard("") }, "Connect a provider"},
		{"routing", func(m *model) { m.openRouting(rtPreset) }, "Model routing"},
	}
	for _, tc := range openers {
		t.Run(tc.name, func(t *testing.T) {
			m := testModel(t)
			m.setWidth(80)
			m.Update(outputMsg("visible conversation"))
			tc.open(m)
			for _, height := range []int{30, 16} {
				m.height = height
				checkPanelLayout(t, m, tc.want)
			}
			m.input.SetValue("first\nsecond\nthird")
			checkPanelLayout(t, m, tc.want)
		})
	}
}

func TestLongPanelListsKeepSelectionVisible(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	m.height = 16
	m.Update(outputMsg("visible conversation"))
	m.openSettings("")
	for i := range 40 {
		m.settings.list.items = append(m.settings.list.items, pickItem{label: fmt.Sprintf("extra setting %02d", i), value: i})
	}
	m.settings.list.selectWhere(func(it pickItem) bool { return it.value == 39 })
	checkPanelLayout(t, m, "extra setting 39")
	m.settings = nil
	m.openProviders()
	for i := range 40 {
		m.provs.list.items = append(m.provs.list.items, pickItem{label: fmt.Sprintf("extra provider %02d", i), value: i})
	}
	m.provs.list.selectWhere(func(it pickItem) bool { return it.value == 39 })
	checkPanelLayout(t, m, "extra provider 39")
	m.provs = nil
	m.openWizard("")
	for i := range 40 {
		m.wizard.provList.items = append(m.wizard.provList.items, pickItem{label: fmt.Sprintf("extra wizard provider %02d", i), value: i})
	}
	m.wizard.provList.selectWhere(func(it pickItem) bool { return it.value == 39 })
	checkPanelLayout(t, m, "extra wizard provider 39")
	m.wizard = nil
	m.openRouting(rtPreset)
	m.routing.presets.selectWhere(func(it pickItem) bool { return it.label == "Edit current" })
	checkPanelLayout(t, m, "Edit current")
}

func TestLongPanelContentScrollsWithoutMovingComposer(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	m.height = 16
	m.Update(outputMsg("visible conversation"))
	m.openRouting(rtRoles)
	m.routing.err = strings.Repeat("long error detail ", 20)
	before := plain(m.View().Content)
	if m.panelView.TotalLineCount() <= m.panelView.Height() {
		t.Fatal("test panel should exceed its available rows")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyPgDown, Mod: tea.ModCtrl})
	after := plain(m.View().Content)
	if before == after || m.panelView.YOffset() == 0 {
		t.Fatal("Ctrl+Page Down should scroll overflowing panel content")
	}
	if !strings.Contains(after, "visible conversation") || !strings.HasSuffix(after, plain(m.composerView()+"\n"+m.statusLine())) {
		t.Fatalf("scrolling the panel moved the conversation or composer: %q", after)
	}
}

func checkPanelLayout(t *testing.T, m *model, panelText string) {
	t.Helper()
	view := plain(m.View().Content)
	if got := len(strings.Split(view, "\n")); got != m.height {
		t.Fatalf("panel layout has %d rows, want %d: %q", got, m.height, view)
	}
	if !strings.HasSuffix(view, plain(m.composerView()+"\n"+m.statusLine())) {
		t.Fatalf("composer and status should remain at bottom: %q", view)
	}
	if !strings.Contains(view, "visible conversation") || !strings.Contains(view, panelText) {
		t.Fatalf("conversation and %q should both be visible: %q", panelText, view)
	}
	if m.view.Height() < 3 {
		t.Fatalf("panel left only %d conversation rows", m.view.Height())
	}
}

func assertPanelAtBottom(t *testing.T, m *model, panel string) {
	t.Helper()
	view := plain(m.View().Content)
	want := plain(panel + "\n" + m.composerView() + "\n" + m.statusLine())
	if !strings.HasSuffix(view, want) {
		t.Fatalf("picker should sit directly above composer: %q", view)
	}
	if !strings.Contains(view, "visible conversation") {
		t.Fatalf("conversation should remain visible above picker: %q", view)
	}
	if got := len(strings.Split(view, "\n")); got != m.height {
		t.Fatalf("picker layout has %d rows, want %d", got, m.height)
	}
}

func TestConversationScrollsAboveComposer(t *testing.T) {
	m := testModel(t)
	m.setWidth(80)
	m.height = 18
	m.Update(outputMsg("first response"))
	initial := m.View()
	if !initial.AltScreen || !strings.Contains(plain(initial.Content), "first response") {
		t.Fatalf("response should render in the conversation viewport: %q", plain(initial.Content))
	}
	var history strings.Builder
	for i := range 50 {
		fmt.Fprintf(&history, "line %02d\n", i)
	}
	m.Update(outputMsg(history.String()))
	bottom := plain(m.View().Content)
	if !strings.Contains(bottom, "line 49") || strings.Contains(bottom, "line 00") {
		t.Fatalf("viewport should follow the latest output: %q", bottom)
	}
	m.Update(press(tea.KeyPgUp))
	older := plain(m.View().Content)
	if older == bottom || !strings.Contains(older, "line 3") {
		t.Fatalf("Page Up should show older output: %q", older)
	}
	m.Update(outputMsg("latest while scrolled"))
	if strings.Contains(plain(m.View().Content), "latest while scrolled") {
		t.Fatal("new output should not jump a scrolled viewport to the bottom")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl})
	if !strings.Contains(plain(m.View().Content), "latest while scrolled") {
		t.Fatal("Ctrl+End should return to the newest output")
	}
	m.running = true
	m.stream.WriteString("streaming answer")
	if !strings.Contains(plain(m.View().Content), "streaming answer") {
		t.Fatal("live answer should share the conversation viewport")
	}
}

func checkPinnedView(t *testing.T, m *model) {
	t.Helper()
	view := plain(m.View().Content)
	lines := strings.Split(view, "\n")
	if len(lines) != m.height {
		t.Fatalf("view has %d rows, want %d", len(lines), m.height)
	}
	bottom := plain(m.composerView() + "\n" + m.statusLine())
	if !strings.HasSuffix(view, bottom) {
		t.Fatalf("composer and footer are not at bottom: %q", view)
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
	m.handleEvent(agent.Event{Kind: agent.EvToolStart, ToolID: "child", ToolName: "read", Agent: "explore: find UI", Model: "haiku-4-5", Input: []byte(`{"path":"view.go"}`)})
	view := plain(m.liveView())
	// The row names the model each subagent runs on; one inherits the main model.
	for _, want := range []string{"Subagents (2 running)", "explore: find UI · haiku-4-5", "general-purpose: write tests · m", "read(view.go)", "0 tools"} {
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

func TestFooterShowsSandboxEffortBarAndBudget(t *testing.T) {
	m := testModel(t)
	m.width = 140
	m.agent.SetEffort(llm.EffortMax)
	m.stats = agent.UsageInfo{CostUSD: 0.68}
	m.opts.Config.Budget = config.Budget{SessionUSD: 2}

	got := plain(m.statusLine())
	for _, want := range []string{"⚠ no sandbox", "effort max █", "$0.68/$2.00"} {
		if !strings.Contains(got, want) {
			t.Errorf("footer lacks %q: %q", want, got)
		}
	}

	// Amber at the warn threshold, red at the cap, plain below it.
	m.stats.CostUSD = 1.7
	warn := m.costText()
	m.stats.CostUSD = 2.1
	capped := m.costText()
	m.stats.CostUSD = 0.5
	low := m.costText()
	if warn == plain(warn) || capped == plain(capped) || warn == capped {
		t.Errorf("cost should be colored differently at warn and cap: %q %q", warn, capped)
	}
	if low != plain(low) {
		t.Errorf("cost below the warn threshold should be plain: %q", low)
	}
}

func TestToolTitleShowsTheToolBehindCallTool(t *testing.T) {
	got := toolTitle(tools.CallToolName, []byte(`{"tool":"mcp__github__create_issue","arguments":{"title":"Bug"}}`), nil)
	if !strings.Contains(got, "github › create_issue") || strings.Contains(got, "call_tool") {
		t.Errorf("a deferred tool should be titled as itself: %s", got)
	}
	if got := toolTitle(tools.ToolSearchName, []byte(`{"query":"create issue"}`), nil); !strings.Contains(got, "create issue") {
		t.Errorf("tool_search title: %s", got)
	}
}
