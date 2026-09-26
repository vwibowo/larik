package subagent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

// bgHarness wires a parent agent whose children block until gate closes.
type bgHarness struct {
	t      *testing.T
	fp     *funcProvider
	a      *agent.Agent
	gate   chan struct{}
	mu     sync.Mutex
	bgEvts []agent.Event
	done   chan agent.Event
}

func newBG(t *testing.T, mode permission.Mode, parent func(req llm.Request) llm.Message) *bgHarness {
	h := &bgHarness{t: t, gate: make(chan struct{}), done: make(chan agent.Event, 16)}
	h.fp = &funcProvider{gate: h.gate}
	h.fp.respond = func(req llm.Request) llm.Message {
		if isChild(req) {
			if strings.Contains(req.Messages[0].Text(), "needs bash") && len(toolResults(req)) == 0 {
				return llm.Message{Blocks: []llm.Block{use("c1", "bash", `{"command":"touch from-bg.txt"}`)}}
			}
			return text("child result for: " + req.Messages[0].Text())
		}
		return parent(req)
	}
	h.a, _, _ = newParent(t, h.fp, mode)
	// Replace the tool set to include the wait/stop tools.
	go func() {
		for e := range h.a.Background() {
			if e.Kind == agent.EvPermission {
				e.Reply <- agent.PermissionReply{Allow: true}
			}
			h.mu.Lock()
			h.bgEvts = append(h.bgEvts, e)
			h.mu.Unlock()
			if e.Kind == agent.EvTaskDone {
				h.done <- e
			}
		}
	}()
	t.Cleanup(h.a.StopAllBackground)
	return h
}

func (h *bgHarness) waitDone() agent.Event {
	h.t.Helper()
	select {
	case e := <-h.done:
		return e
	case <-time.After(5 * time.Second):
		h.t.Fatal("background task never finished")
		return agent.Event{}
	}
}

func lastUserText(req llm.Request) string {
	var b strings.Builder
	for _, blk := range req.Messages[len(req.Messages)-1].Blocks {
		b.WriteString(blk.Text + blk.Content)
	}
	return b.String()
}

func startBG(prompt string) llm.Message {
	return llm.Message{Blocks: []llm.Block{use("p1", "task", `{"description":"bg job","prompt":"`+prompt+`","subagent_type":"general-purpose","run_in_background":true}`)}}
}

func TestBackgroundDeliveredWhenIdle(t *testing.T) {
	h := newBG(t, permission.ModeYolo, func(req llm.Request) llm.Message {
		switch {
		case strings.Contains(lastUserText(req), "<task-notification"):
			return text("got it")
		case len(toolResults(req)) > 0:
			return text("started; I'll carry on")
		}
		return startBG("research X")
	})

	evs := drain(h.a.Run(context.Background(), "go"), true)
	if evs[len(evs)-1].StopReason != "end_turn" {
		t.Fatalf("foreground turn should end while child runs")
	}
	reqs := h.fp.requests()
	if res := toolResults(reqs[len(reqs)-1]); !strings.Contains(res[0].Content, "Started background task bg-1") {
		t.Fatalf("task result = %+v", res)
	}
	if h.a.RunningBackground() != 1 {
		t.Fatal("child should still be running")
	}

	close(h.gate)
	done := h.waitDone()
	if done.ToolID != "bg-1" || done.StopReason != "completed" || !strings.Contains(done.Output, "child result for: research X") {
		t.Fatalf("done = %+v", done)
	}
	if h.a.PendingNotifications() != 1 {
		t.Fatal("result should be pending delivery")
	}
	ch, ok := h.a.RunNotifications(context.Background())
	if !ok {
		t.Fatal("expected a notification turn")
	}
	drain(ch, true)
	reqs = h.fp.requests()
	last := lastUserText(reqs[len(reqs)-1])
	if !strings.Contains(last, `<task-notification id="bg-1" agent="general-purpose: bg job" status="completed">`) || !strings.Contains(last, "child result for: research X") {
		t.Fatalf("notification turn prompt:\n%s", last)
	}
	if _, ok := h.a.RunNotifications(context.Background()); ok {
		t.Error("results must be delivered only once")
	}
}

func TestBackgroundInjectedMidTurn(t *testing.T) {
	var h *bgHarness
	step := 0
	h = newBG(t, permission.ModeYolo, func(req llm.Request) llm.Message {
		step++
		switch step {
		case 1:
			return startBG("side job")
		case 2: // child still gated: do foreground work, then let it finish
			close(h.gate)
			h.waitDone()
			return llm.Message{Blocks: []llm.Block{use("p2", "glob", `{"pattern":"*"}`)}}
		default:
			return text("done")
		}
	})
	drain(h.a.Run(context.Background(), "go"), true)
	reqs := h.fp.requests()
	var parentReqs []llm.Request
	for _, r := range reqs {
		if !isChild(r) {
			parentReqs = append(parentReqs, r)
		}
	}
	last := lastUserText(parentReqs[len(parentReqs)-1])
	if !strings.Contains(last, "<task-notification") || !strings.Contains(last, "child result for: side job") {
		t.Fatalf("result should ride along with the next request:\n%s", last)
	}
	if h.a.PendingNotifications() != 0 {
		t.Error("nothing should remain pending")
	}
}

func TestTaskWaitAndStop(t *testing.T) {
	h := newBG(t, permission.ModeYolo, nil)
	step := 0
	h.fp.respond = func(req llm.Request) llm.Message {
		if isChild(req) {
			return text("child result for: " + req.Messages[0].Text())
		}
		step++
		switch step {
		case 1:
			return llm.Message{Blocks: []llm.Block{
				use("p1", "task", `{"description":"a","prompt":"job A","subagent_type":"explore","run_in_background":true}`),
				use("p2", "task", `{"description":"b","prompt":"job B","subagent_type":"explore","run_in_background":true}`),
			}}
		case 2: // ids follow start order, which is parallel: look them up by label
			return llm.Message{Blocks: []llm.Block{use("p3", "task_stop", `{"id":"`+idOf(h.a, "explore: b")+`"}`)}}
		case 3:
			go func() { time.Sleep(100 * time.Millisecond); close(h.gate) }()
			return llm.Message{Blocks: []llm.Block{use("p4", "task_wait", `{"ids":["`+idOf(h.a, "explore: a")+`"]}`)}}
		default:
			return text("finished")
		}
	}
	// The parent needs the wait/stop tools in its registry.
	reg := h.a.Tools()
	var ts []tools.Tool
	for _, s := range reg.Specs() {
		tl, _ := reg.Get(s.Name)
		ts = append(ts, tl)
	}
	h.a.SetTools(tools.NewRegistry(append(ts, WaitTool{}, StopTool{})...))

	drain(h.a.Run(context.Background(), "go"), true)
	reqs := h.fp.requests()
	var parent []llm.Request
	for _, r := range reqs {
		if !isChild(r) {
			parent = append(parent, r)
		}
	}
	idA, idB := idOf(h.a, "explore: a"), idOf(h.a, "explore: b")
	if res := toolResults(parent[2]); res[0].Content != "Stopped "+idB+"." {
		t.Errorf("stop = %+v", res)
	}
	wait := toolResults(parent[3])[0].Content
	if !strings.Contains(wait, `<task id="`+idA+`" agent="explore: a" status="completed">`) || !strings.Contains(wait, "child result for: job A") {
		t.Errorf("wait = %s", wait)
	}
	if n := h.a.PendingNotifications(); n != 0 {
		t.Errorf("waited-on and stopped tasks must not be re-delivered; pending=%d", n)
	}
	statuses := map[string]agent.BgStatus{}
	for _, tk := range h.a.BackgroundTasks() {
		statuses[tk.ID] = tk.Status
	}
	if statuses[idA] != agent.BgCompleted || statuses[idB] != agent.BgStopped {
		t.Errorf("statuses = %v", statuses)
	}
}

func TestBackgroundPermissionsAndLimit(t *testing.T) {
	h := newBG(t, permission.ModeDefault, func(req llm.Request) llm.Message {
		if len(toolResults(req)) > 0 {
			return text("ok")
		}
		return startBG("needs bash")
	})
	drain(h.a.Run(context.Background(), "go"), false) // foreground denies; background allows
	close(h.gate)
	done := h.waitDone()
	if done.StopReason != "completed" {
		t.Fatalf("done = %+v", done)
	}
	h.mu.Lock()
	asked := false
	for _, e := range h.bgEvts {
		if e.Kind == agent.EvPermission && e.Agent == "general-purpose: bg job" {
			asked = true
		}
	}
	h.mu.Unlock()
	if !asked {
		t.Error("a background child's permission prompt must arrive on Background()")
	}

	// Limit on concurrently running tasks.
	block := make(chan struct{})
	defer close(block)
	for i := 0; i < agent.MaxBackground; i++ {
		if _, err := h.a.StartBackground("x", func(ctx context.Context, _ func(agent.Event)) (string, bool) {
			select {
			case <-block:
			case <-ctx.Done():
			}
			return "", false
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.a.StartBackground("one too many", func(context.Context, func(agent.Event)) (string, bool) { return "", false }); err == nil {
		t.Error("expected the background limit to apply")
	}
}

func idOf(a *agent.Agent, label string) string {
	for _, t := range a.BackgroundTasks() {
		if t.Label == label {
			return t.ID
		}
	}
	return "missing"
}

func TestInterruptKeepsBackgroundRunning(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	step := 0
	h := newBG(t, permission.ModeYolo, func(req llm.Request) llm.Message {
		step++
		if step == 1 {
			return startBG("survive")
		}
		cancel() // user presses Esc during the next foreground request
		return text("interrupted")
	})
	evs := drain(h.a.Run(ctx, "go"), true)
	if evs[len(evs)-1].StopReason != "interrupted" {
		t.Fatalf("stop = %s", evs[len(evs)-1].StopReason)
	}
	close(h.gate)
	if done := h.waitDone(); done.StopReason != "completed" || !strings.Contains(done.Output, "survive") {
		t.Fatalf("background task should survive a foreground interrupt: %+v", done)
	}
}
