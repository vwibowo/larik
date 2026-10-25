package server

import (
	"context"
	"errors"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"larik/internal/agent"
	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/providers"
	"larik/internal/session"
)

func awaitShutdown(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for shutdown progress")
	}
}

func canceledShutdown(s *Server) error {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return s.Shutdown(ctx)
}

func apiRequest(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func TestShutdownZeroServer(t *testing.T) {
	var s Server
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestShutdownRetainsLockUntilUncooperativeRunExits(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	l := h.srv.lookup(st.ID)
	stream := h.stream(st.ID, "")
	started, canceled, release, exited := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	go func() {
		defer close(exited)
		_ = l.idleDo(context.Background(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			<-release
			return nil
		})
	}()
	awaitShutdown(t, started)
	if err := canceledShutdown(h.srv); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown = %v", err)
	}
	awaitShutdown(t, canceled)
	// Existing SSE endpoints must end even while the writer ignores cancellation.
	ended := make(chan struct{})
	go func() {
		for range stream.events {
		}
		close(ended)
	}()
	awaitShutdown(t, ended)
	if _, err := l.prompt("new work"); err == nil {
		t.Fatal("closed session accepted work")
	}
	if w := apiRequest(h.srv, "POST", "/v1/sessions", "{}"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("create = %d", w.Code)
	}
	if file, _, err := session.Open(l.s.Path); err == nil {
		file.Close()
		t.Fatal("transcript lock released while writer active")
	}
	// Every duplicate close waits for real cleanup, not merely closed=true.
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() { defer wg.Done(); l.close(); h.srv.Close() }()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("duplicate closes returned before writer exited")
	default:
	}
	unblock()
	awaitShutdown(t, exited)
	awaitShutdown(t, done)
	awaitShutdown(t, l.drained)
	if err := h.srv.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	file, _, err := session.Open(l.s.Path)
	if err != nil {
		t.Fatalf("transcript still locked after cleanup: %v", err)
	}
	file.Close()
}

func TestShutdownCancelsBackgroundBeforeForegroundExits(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	l := h.srv.lookup(st.ID)
	taskStarted, taskCanceled, taskRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(taskRelease) }) }
	defer unblock()
	runStarted := make(chan struct{})
	go func() {
		_ = l.idleDo(context.Background(), func(ctx context.Context) error {
			close(runStarted)
			<-ctx.Done()
			<-taskRelease // foreground ignores cancellation too
			return nil
		})
	}()
	awaitShutdown(t, runStarted)
	_, err := l.a.StartBackground("stalled writer", func(ctx context.Context, _ func(agent.Event)) (string, bool) {
		close(taskStarted)
		<-ctx.Done()
		close(taskCanceled)
		<-taskRelease
		return "done", false
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitShutdown(t, taskStarted)
	if err := canceledShutdown(h.srv); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown = %v", err)
	}
	awaitShutdown(t, taskCanceled)
	if file, _, err := session.Open(l.s.Path); err == nil {
		file.Close()
		t.Fatal("lock released before background writer exits")
	}
	unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownCooperativeRunAndTask(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	l := h.srv.lookup(st.ID)
	started := make(chan struct{})
	go func() {
		_ = l.idleDo(context.Background(), func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() })
	}()
	awaitShutdown(t, started)
	_, err := l.a.StartBackground("cooperative", func(ctx context.Context, _ func(agent.Event)) (string, bool) { <-ctx.Done(); return "", false })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	awaitShutdown(t, l.drained)
}

func TestShutdownWaitsForAlreadyClosingSession(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	l := h.srv.lookup(st.ID)
	started, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	go func() {
		_ = l.idleDo(context.Background(), func(ctx context.Context) error { close(started); <-ctx.Done(); close(canceled); <-release; return nil })
	}()
	awaitShutdown(t, started)
	deleted := make(chan struct{})
	go func() { apiRequest(h.srv, "DELETE", "/v1/sessions/"+st.ID, ""); close(deleted) }()
	awaitShutdown(t, canceled) // DELETE has removed the session from the map.
	if err := canceledShutdown(h.srv); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown ignored in-flight DELETE: %v", err)
	}
	unblock()
	awaitShutdown(t, deleted)
	h.srv.Close()
}

func TestShutdownWaitsForOpeningSessions(t *testing.T) {
	for _, fork := range []bool{false, true} {
		name := "create"
		if fork {
			name = "fork"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			st := h.create(map[string]any{})
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			h.srv.app.Resolve = func(*config.Config, string) (providers.Resolved, error) {
				close(entered)
				<-release
				return providers.Resolved{Provider: fakeLLM{}, Model: "m"}, nil
			}
			path := "/v1/sessions"
			if fork {
				path += "/" + st.ID + "/fork"
			}
			response := make(chan *httptest.ResponseRecorder, 1)
			go func() { response <- apiRequest(h.srv, "POST", path, "{}") }()
			awaitShutdown(t, entered)
			if err := canceledShutdown(h.srv); !errors.Is(err, context.Canceled) {
				t.Fatalf("Shutdown ignored in-flight open: %v", err)
			}
			unblock()
			select {
			case w := <-response:
				if w.Code != http.StatusServiceUnavailable {
					t.Fatalf("opening response = %d: %s", w.Code, w.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("opening did not return")
			}
			h.srv.Close()
			// Registration rejection closes the opened file without holding s.mu.
			infos, err := session.List(h.srv.app.SessionDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, info := range infos {
				file, _, err := session.Open(info.Path)
				if err != nil {
					t.Fatalf("opening leaked transcript lock: %v", err)
				}
				file.Close()
			}
		})
	}
}

// Unlike the usual fakeLLM this provider deliberately ignores cancellation.
// It exercises the actual agent event loop and its final transcript writes.
type stalledShutdownLLM struct {
	entered chan struct{}
	release chan struct{}
}

func (*stalledShutdownLLM) Name() string { return "stalled" }
func (p *stalledShutdownLLM) Stream(_ context.Context, _ llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		close(p.entered)
		<-p.release
		yield(llm.StreamEvent{Type: llm.EventDone, Message: reply(llm.Block{Type: llm.BlockText, Text: "late response"}), StopReason: llm.StopEnd}, nil)
	}
}

func TestShutdownDeadlineWithUncooperativeProvider(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	l := h.srv.lookup(st.ID)
	p := &stalledShutdownLLM{entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(p.release) }) }
	defer unblock()
	l.a.SetModel(p, "m")
	if _, err := l.prompt("hello"); err != nil {
		t.Fatal(err)
	}
	awaitShutdown(t, p.entered)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := h.srv.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v", err)
	}
	if file, _, err := session.Open(l.s.Path); err == nil {
		file.Close()
		t.Fatal("lock released while model still running")
	}
	unblock()
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := h.srv.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	awaitShutdown(t, l.drained)
}
