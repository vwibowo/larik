package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"larik/internal/agent"
	"larik/internal/app"
	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/providers"
)

const token = "test-token"

// fakeLLM answers by the last user message: "hello" gets text, "write"
// asks for a file write (which needs permission), "slow" blocks until
// cancelled; a tool result gets "done".
type fakeLLM struct{}

func (fakeLLM) Name() string { return "fake" }

func (fakeLLM) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		last := req.Messages[len(req.Messages)-1]
		var msg llm.Message
		switch {
		case strings.Contains(last.Text(), "write"):
			msg = reply(llm.Block{Type: llm.BlockToolUse, ID: "t1", Name: "write", Input: json.RawMessage(`{"path":"made.txt","content":"x"}`)})
		case hasToolResult(last):
			msg = reply(llm.Block{Type: llm.BlockText, Text: "done"})
		case strings.Contains(last.Text(), "slow"):
			<-ctx.Done()
			yield(llm.StreamEvent{}, ctx.Err())
			return
		default:
			msg = reply(llm.Block{Type: llm.BlockText, Text: "hi there"})
		}
		if t := msg.Text(); t != "" && !yield(llm.StreamEvent{Type: llm.EventTextDelta, Text: t}, nil) {
			return
		}
		stop := llm.StopEnd
		if len(msg.ToolUses()) > 0 {
			stop = llm.StopToolUse
		}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: stop, Usage: llm.Usage{Input: 10, Output: 5}}, nil)
	}
}

func reply(b llm.Block) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{b}, Model: "m"}
}

func hasToolResult(m llm.Message) bool {
	for _, b := range m.Blocks {
		if b.Type == llm.BlockToolResult {
			return true
		}
	}
	return false
}

type harness struct {
	t   *testing.T
	url string
	cwd string
	srv *Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	cfgHome, dataHome, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	// No sandbox: sandboxed bash would be auto-allowed, and these tests
	// exercise permission prompts.
	os.MkdirAll(filepath.Join(cfgHome, "larik"), 0o755)
	os.WriteFile(filepath.Join(cfgHome, "larik", "config.json"), []byte(`{"sandbox":{"enabled":false}}`), 0o644)

	a, err := app.Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.Resolve = func(*config.Config, string) (providers.Resolved, error) {
		return providers.Resolved{Provider: fakeLLM{}, Model: "m"}, nil
	}
	srv := New(a, Options{Token: token, Keepalive: 50 * time.Millisecond})
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		srv.Close()
		hs.Close()
		a.Close()
	})
	return &harness{t: t, url: hs.URL, cwd: cwd, srv: srv}
}

func (h *harness) do(method, path string, body any, out any) int {
	h.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, h.url+path, rd)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (h *harness) create(body any) sessionState {
	h.t.Helper()
	var st sessionState
	if code := h.do("POST", "/v1/sessions", body, &st); code != http.StatusCreated {
		h.t.Fatalf("create: %d", code)
	}
	return st
}

// sse reads events from a session stream.
type sse struct {
	resp   *http.Response
	events chan Event
}

func (h *harness) stream(id, after string) *sse {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.url+"/v1/sessions/"+id+"/events?token="+token, nil)
	if after != "" {
		req.Header.Set("Last-Event-ID", after)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		h.t.Fatalf("stream: %v %v", err, resp)
	}
	s := &sse{resp: resp, events: make(chan Event, 256)}
	go func() {
		defer close(s.events)
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var e Event
				json.Unmarshal([]byte(data), &e)
				s.events <- e
			}
		}
	}()
	h.t.Cleanup(func() { resp.Body.Close() })
	return s
}

// until returns events up to and including the first one matching.
func (s *sse) until(t *testing.T, match func(Event) bool) []Event {
	t.Helper()
	var got []Event
	timeout := time.After(5 * time.Second)
	for {
		select {
		case e, ok := <-s.events:
			if !ok {
				t.Fatalf("stream ended; got %v", kinds(got))
			}
			got = append(got, e)
			if match(e) {
				return got
			}
		case <-timeout:
			t.Fatalf("timed out; got %v", kinds(got))
		}
	}
}

func idle(e Event) bool { return e.Kind == EvStatus && !*e.Busy }

func kinds(es []Event) []string {
	var out []string
	for _, e := range es {
		out = append(out, string(e.Kind))
	}
	return out
}

func TestAuthAndHost(t *testing.T) {
	h := newHarness(t)
	resp, _ := http.Get(h.url + "/v1/health")
	if resp.StatusCode != 200 {
		t.Errorf("health: %d", resp.StatusCode)
	}
	resp, _ = http.Get(h.url + "/v1/sessions")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no token: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("GET", h.url+"/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Host = "evil.example:80"
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("rebinding host: %d", resp.StatusCode)
	}
	// The query-string token is only for the event stream.
	resp, _ = http.Get(h.url + "/v1/sessions?token=" + token)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("query token outside /events: %d", resp.StatusCode)
	}
}

func TestEmptyServerTokenIsNotAccepted(t *testing.T) {
	s := New(nil, Options{})
	req := httptest.NewRequest("GET", "http://localhost/v1/info", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("empty token: status %d, want 401", w.Code)
	}
}

func TestJSONBodyRequiresExactlyOneValue(t *testing.T) {
	h := newHarness(t)
	for _, body := range []string{"", "{}{}", "{} trailing"} {
		req, _ := http.NewRequest("POST", h.url+"/v1/sessions", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status %d, want 400", body, resp.StatusCode)
		}
	}
}

func TestCompactReturnsAndPersistsMetrics(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "hello", "wait": true}, nil)
	var res struct {
		Summary    string
		Compaction agent.CompactionInfo
	}
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/compact", nil, &res); code != http.StatusOK {
		t.Fatalf("compact: %d", code)
	}
	if res.Summary != "hi there" || res.Compaction.BeforeTokens != 10 || res.Compaction.AfterTokens != 5 || res.Compaction.SavedTokens != 5 || !res.Compaction.Available {
		t.Fatalf("compact response: %+v", res)
	}
	if code := h.do("GET", "/v1/sessions/"+st.ID, nil, &st); code != http.StatusOK {
		t.Fatalf("get session: %d", code)
	}
	if st.Usage.Compactions != 1 || st.Usage.CompactionMeasurements != 1 || st.Usage.CompactionSavedTokens != 5 {
		t.Fatalf("session compaction stats: %+v", st.Usage)
	}
}

func TestPromptWait(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	if st.Model != "m" || st.Mode != "default" || st.Busy {
		t.Errorf("state: %+v", st)
	}
	var res struct {
		Text       string
		StopReason string `json:"stop_reason"`
		Error      string
	}
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "hello", "wait": true}, &res); code != 200 {
		t.Fatalf("prompt: %d", code)
	}
	if res.Text != "hi there" || res.Error != "" || res.StopReason != "end_turn" {
		t.Errorf("result: %+v", res)
	}
	var msgs struct{ Messages []llm.Message }
	h.do("GET", "/v1/sessions/"+st.ID+"/messages", nil, &msgs)
	if len(msgs.Messages) != 2 || msgs.Messages[1].Text() != "hi there" {
		t.Errorf("messages: %+v", msgs)
	}
	var list struct{ Sessions []sessionInfo }
	h.do("GET", "/v1/sessions", nil, &list)
	if len(list.Sessions) != 1 || !list.Sessions[0].Loaded || list.Sessions[0].Title != "hello" {
		t.Errorf("list: %+v", list)
	}
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": " "}, nil); code != 400 {
		t.Errorf("empty prompt: %d", code)
	}
	if code := h.do("PATCH", "/v1/sessions/"+st.ID, map[string]any{"mode": "bogus"}, nil); code != 400 {
		t.Errorf("bad mode: %d", code)
	}
	if code := h.do("PATCH", "/v1/sessions/"+st.ID, map[string]any{"mode": "plan", "effort": "high"}, &st); code != 200 || st.Mode != "plan" || st.Effort != "high" {
		t.Errorf("patch: %d %+v", code, st)
	}
}

func TestPermissionOverSSE(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	s := h.stream(st.ID, "")
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "please write"}, nil); code != http.StatusAccepted {
		t.Fatalf("prompt: %d", code)
	}
	evs := s.until(t, func(e Event) bool { return e.Kind == agent.EvPermission })
	perm := evs[len(evs)-1]
	if perm.RequestID == "" || perm.ToolName != "write" || evs[0].Kind != EvStatus || evs[1].Kind != EvUserMessage {
		t.Fatalf("events: %v %+v", kinds(evs), perm)
	}

	// A second prompt or model change while busy is refused; mode changes
	// remain available to resolve permission requests.
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "hello"}, nil); code != http.StatusConflict {
		t.Errorf("busy prompt: %d", code)
	}
	if code := h.do("PATCH", "/v1/sessions/"+st.ID, map[string]any{"model": "other"}, nil); code != http.StatusConflict {
		t.Errorf("busy model change: %d", code)
	}
	if code := h.do("PATCH", "/v1/sessions/"+st.ID, map[string]any{"mode": "accept-edits"}, nil); code != http.StatusOK {
		t.Errorf("busy mode change: %d", code)
	}
	var pending struct{ Permissions []Event }
	h.do("GET", "/v1/sessions/"+st.ID+"/permissions", nil, &pending)
	if len(pending.Permissions) != 1 || pending.Permissions[0].RequestID != perm.RequestID {
		t.Errorf("pending: %+v", pending)
	}

	if code := h.do("POST", "/v1/sessions/"+st.ID+"/permissions/"+perm.RequestID, map[string]any{"allow": true}, nil); code != http.StatusNoContent {
		t.Fatalf("answer: %d", code)
	}
	evs = s.until(t, idle)
	var resolved, toolEnd bool
	for _, e := range evs {
		resolved = resolved || (e.Kind == EvPermResolved && e.Text == "allowed")
		toolEnd = toolEnd || (e.Kind == agent.EvToolEnd && !e.IsError)
	}
	if !resolved || !toolEnd {
		t.Errorf("after answer: %v", kinds(evs))
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "made.txt")); err != nil {
		t.Error("write did not run")
	}
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/permissions/"+perm.RequestID, map[string]any{"allow": true}, nil); code != 404 {
		t.Errorf("answering twice: %d", code)
	}
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/permissions/"+perm.RequestID, map[string]any{"allow": true, "mode": "sideways"}, nil); code != http.StatusBadRequest {
		t.Errorf("an unknown mode: %d", code)
	}

	// Reconnecting with Last-Event-ID replays what was missed.
	replay := h.stream(st.ID, "0")
	all := replay.until(t, idle)
	if all[0].Seq != 1 || all[len(all)-1].Seq != evs[len(evs)-1].Seq {
		t.Errorf("replay: %v", kinds(all))
	}
}

func TestAlwaysAllowReportsPersistenceAndReloads(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	s := h.stream(st.ID, "")
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "please write"}, nil); code != http.StatusAccepted {
		t.Fatalf("prompt: %d", code)
	}
	evs := s.until(t, func(e Event) bool { return e.Kind == agent.EvPermission })
	perm := evs[len(evs)-1]
	var answer struct {
		Allowed   bool `json:"allowed"`
		Persisted bool `json:"persisted"`
	}
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/permissions/"+perm.RequestID, map[string]any{"allow": true, "always": true}, &answer); code != http.StatusOK || !answer.Allowed || !answer.Persisted {
		t.Fatalf("always answer: %d %+v", code, answer)
	}
	s.until(t, idle)
	cfg, err := config.Load(h.cwd)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(cfg.Permissions.Allow, "write") {
		t.Fatalf("reloaded settings lost the rule: %v", cfg.Permissions.Allow)
	}
}

func TestAlwaysAllowSaveFailureIsVisibleToHTTPAndSSE(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	s := h.stream(st.ID, "")
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "please write"}, nil); code != http.StatusAccepted {
		t.Fatalf("prompt: %d", code)
	}
	evs := s.until(t, func(e Event) bool { return e.Kind == agent.EvPermission })
	perm := evs[len(evs)-1]
	if err := os.MkdirAll(config.LocalSettingsPath(h.cwd), 0o700); err != nil {
		t.Fatal(err)
	}
	var answer struct {
		Allowed   bool   `json:"allowed"`
		Persisted bool   `json:"persisted"`
		Error     string `json:"error"`
	}
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/permissions/"+perm.RequestID, map[string]any{"allow": true, "always": true}, &answer); code != http.StatusOK || !answer.Allowed || answer.Persisted || answer.Error == "" {
		t.Fatalf("failed save answer: %d %+v", code, answer)
	}
	evs = s.until(t, idle)
	var warned bool
	for _, ev := range evs {
		if ev.Kind == agent.EvNotice && strings.Contains(ev.Text, "Could not save always-allow rule") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("SSE did not report save failure: %v", kinds(evs))
	}
}

func TestCancelAndDeny(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	s := h.stream(st.ID, "")

	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "slow"}, nil)
	s.until(t, func(e Event) bool { return e.Kind == EvUserMessage })
	var c struct{ Cancelled bool }
	h.do("POST", "/v1/sessions/"+st.ID+"/cancel", nil, &c)
	if !c.Cancelled {
		t.Error("cancel reported nothing to cancel")
	}
	evs := s.until(t, idle)
	var done bool
	for _, e := range evs {
		done = done || e.Kind == agent.EvDone
	}
	if !done {
		t.Errorf("cancel: %v", kinds(evs))
	}

	// A permission request left pending when the run is cancelled expires.
	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "write"}, nil)
	s.until(t, func(e Event) bool { return e.Kind == agent.EvPermission })
	h.do("POST", "/v1/sessions/"+st.ID+"/cancel", nil, nil)
	evs = s.until(t, idle)
	var expired bool
	for _, e := range evs {
		expired = expired || (e.Kind == EvPermResolved && e.Text == "expired")
	}
	if !expired {
		t.Errorf("expire: %v", kinds(evs))
	}

	// Denying gives the model the feedback and the file is not written.
	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "write"}, nil)
	p := s.until(t, func(e Event) bool { return e.Kind == agent.EvPermission })
	h.do("POST", "/v1/sessions/"+st.ID+"/permissions/"+p[len(p)-1].RequestID, map[string]any{"allow": false, "reason": "not now"}, nil)
	evs = s.until(t, idle)
	var denied bool
	for _, e := range evs {
		denied = denied || (e.Kind == agent.EvToolEnd && e.IsError && strings.Contains(e.Output, "not now"))
	}
	if !denied {
		t.Errorf("deny: %v", kinds(evs))
	}
	if _, err := os.Stat(filepath.Join(h.cwd, "made.txt")); err == nil {
		t.Error("denied write ran")
	}
}

func TestCloseAndResume(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "hello", "wait": true}, nil)
	s := h.stream(st.ID, "")

	if code := h.do("DELETE", "/v1/sessions/"+st.ID, nil, nil); code != http.StatusNoContent {
		t.Fatalf("close: %d", code)
	}
	s.until(t, func(e Event) bool { return e.Kind == EvClosed })
	if code := h.do("GET", "/v1/sessions/"+st.ID, nil, nil); code != 404 {
		t.Errorf("closed session still loaded: %d", code)
	}
	// Messages are still readable from the file.
	var msgs struct{ Messages []llm.Message }
	if h.do("GET", "/v1/sessions/"+st.ID+"/messages", nil, &msgs); len(msgs.Messages) != 2 {
		t.Errorf("messages after close: %d", len(msgs.Messages))
	}

	var again sessionState
	if code := h.do("POST", "/v1/sessions", map[string]any{"resume": st.ID[:8]}, &again); code != http.StatusCreated || again.ID != st.ID {
		t.Fatalf("resume: %d %+v", code, again)
	}
	// Loading an already-loaded session returns it rather than opening the
	// file a second time.
	if code := h.do("POST", "/v1/sessions", map[string]any{"continue": true}, &again); code != http.StatusOK || again.ID != st.ID {
		t.Errorf("continue loaded: %d %+v", code, again)
	}
	var res struct{ Text string }
	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "hello again", "wait": true}, &res)
	h.do("GET", "/v1/sessions/"+st.ID+"/messages", nil, &msgs)
	if res.Text != "hi there" || len(msgs.Messages) != 4 {
		t.Errorf("after resume: %q %d", res.Text, len(msgs.Messages))
	}
	if code := h.do("POST", "/v1/sessions", map[string]any{"resume": "nope"}, nil); code != 404 {
		t.Errorf("unknown resume: %d", code)
	}
}

func TestBusDropsSlowSubscriber(t *testing.T) {
	b := newBus()
	_, slow, _ := b.subscribe(0)
	for range subQueue + 1 {
		b.publish(Event{})
	}
	n := 0
	for range slow {
		n++
	}
	if n != subQueue {
		t.Errorf("slow subscriber got %d events before being dropped", n)
	}
	// Replay beyond the ring reports a gap.
	for range ringSize {
		b.publish(Event{})
	}
	replay, _, gap := b.subscribe(0)
	if !gap || len(replay) != ringSize {
		t.Errorf("gap=%v replay=%d", gap, len(replay))
	}
}

func TestBackgroundResultsDeliveredWhenIdle(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	s := h.stream(st.ID, "")
	l := h.srv.lookup(st.ID)
	release := make(chan struct{})
	if _, err := l.a.StartBackground("worker", func(ctx context.Context, emit func(agent.Event)) (string, bool) {
		<-release
		return "background result", false
	}); err != nil {
		t.Fatal(err)
	}
	var tasks struct{ Tasks []taskInfo }
	h.do("GET", "/v1/sessions/"+st.ID+"/tasks", nil, &tasks)
	if len(tasks.Tasks) != 1 || tasks.Tasks[0].Status != "running" {
		t.Errorf("tasks: %+v", tasks)
	}
	close(release)
	// The idle session runs a turn on its own to hand the result over.
	evs := s.until(t, idle)
	want := []string{"task_done", "status", "text_delta"}
	if got := kinds(evs); len(got) < 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("events: %v", got)
	}
	var msgs struct{ Messages []llm.Message }
	h.do("GET", "/v1/sessions/"+st.ID+"/messages", nil, &msgs)
	if len(msgs.Messages) != 2 || !strings.Contains(msgs.Messages[0].Text(), "background result") {
		t.Errorf("messages: %+v", msgs.Messages)
	}
}

func TestFork(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{"mode": "accept-edits"})
	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "hello", "wait": true}, nil)
	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "hello again", "wait": true}, nil)

	// Branch off before the second prompt (message index 2).
	var br struct {
		sessionState
		Prompt string
	}
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/fork", map[string]any{"at": 2}, &br); code != http.StatusCreated {
		t.Fatalf("fork: %d", code)
	}
	if br.ID == st.ID || br.ForkOf != st.ID || br.Prompt != "hello again" || br.Mode != "accept-edits" {
		t.Errorf("branch: %+v", br)
	}
	var msgs struct{ Messages []llm.Message }
	h.do("GET", "/v1/sessions/"+br.ID+"/messages", nil, &msgs)
	if len(msgs.Messages) != 2 {
		t.Errorf("branch messages: %d", len(msgs.Messages))
	}
	// The branch is live and independent of its source.
	var res struct{ Text string }
	h.do("POST", "/v1/sessions/"+br.ID+"/prompt", map[string]any{"text": "a different question", "wait": true}, &res)
	h.do("GET", "/v1/sessions/"+st.ID+"/messages", nil, &msgs)
	if res.Text != "hi there" || len(msgs.Messages) != 4 {
		t.Errorf("source after branch prompt: %d", len(msgs.Messages))
	}

	if code := h.do("POST", "/v1/sessions/"+st.ID+"/fork", map[string]any{"at": 1}, nil); code != http.StatusBadRequest {
		t.Errorf("fork mid-turn: %d", code)
	}
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/fork", map[string]any{"at": -1}, nil); code != http.StatusBadRequest {
		t.Errorf("fork at a negative index: %d", code)
	}
	var list struct{ Sessions []sessionInfo }
	h.do("GET", "/v1/sessions", nil, &list)
	var listed bool
	for _, s := range list.Sessions {
		listed = listed || (s.ID == br.ID && s.ForkOf == st.ID)
	}
	if !listed {
		t.Errorf("list: %+v", list)
	}

	// No branching while the source is running.
	s := h.stream(st.ID, "")
	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "slow"}, nil)
	s.until(t, func(e Event) bool { return e.Kind == EvUserMessage })
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/fork", map[string]any{}, nil); code != http.StatusConflict {
		t.Errorf("fork while busy: %d", code)
	}
	h.do("POST", "/v1/sessions/"+st.ID+"/cancel", nil, nil)
	s.until(t, idle)

	// Unloaded sessions can be branched too.
	h.do("DELETE", "/v1/sessions/"+st.ID, nil, nil)
	if code := h.do("POST", "/v1/sessions/"+st.ID+"/fork", map[string]any{}, &br); code != http.StatusCreated || br.ForkOf != st.ID {
		t.Errorf("fork unloaded: %d %+v", code, br)
	}
}

func TestEndedTaskExpiresItsPermissionRequests(t *testing.T) {
	reply := make(chan agent.PermissionReply, 1)
	l := &live{bus: newBus(), pending: map[string]*pendingPerm{
		"perm-1": {ev: Event{Event: agent.Event{Agent: "worker: build"}}, reply: reply, bg: true},
		"perm-2": {ev: Event{Event: agent.Event{Agent: "explore: docs"}}, reply: make(chan agent.PermissionReply, 1), bg: true},
	}}
	l.expireTask("worker: build")
	if _, ok := l.pending["perm-1"]; ok {
		t.Fatal("the ended task's request should expire")
	}
	if _, ok := l.pending["perm-2"]; !ok {
		t.Fatal("another task's request must stay")
	}
	if r := <-reply; r.Allow {
		t.Fatal("an expired request is answered with a denial")
	}
}

// TestBusGapWhenClientIsAhead: a client that saw seq 50 from a previous
// run of the bus reconnects to a fresh one; it must be told, and get
// everything retained, rather than taking seq 1..3 for old events.
func TestBusGapWhenClientIsAhead(t *testing.T) {
	b := newBus()
	for range 3 {
		b.publish(Event{})
	}
	replay, _, gap := b.subscribe(50)
	if !gap || len(replay) != 3 || replay[0].Seq != 1 {
		t.Fatalf("gap=%v replay=%d", gap, len(replay))
	}
	if replay, _, gap := b.subscribe(3); gap || len(replay) != 0 {
		t.Fatalf("a caught-up client: gap=%v replay=%d", gap, len(replay))
	}
}
