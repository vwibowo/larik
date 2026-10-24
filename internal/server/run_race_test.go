package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"larik/internal/agent"
)

// A wait request subscribes before it can take the run lock. The idle event
// from the preceding run must not complete the new request while it is waiting
// for that lock.
func TestPromptWaitIgnoresPrecedingRunTail(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	l := h.srv.lookup(st.ID)

	l.mu.Lock()
	_, oldCancel := context.WithCancel(context.Background())
	l.busy, l.cancel = true, oldCancel

	type response struct {
		code int
		body struct {
			StopReason string `json:"stop_reason"`
		}
		err error
	}
	responses := make(chan response, 1)
	go func() {
		body, _ := json.Marshal(map[string]any{"text": "slow", "wait": true})
		req, _ := http.NewRequest(http.MethodPost, h.url+"/v1/sessions/"+st.ID+"/prompt", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			responses <- response{err: err}
			return
		}
		defer resp.Body.Close()
		out := response{code: resp.StatusCode}
		out.err = json.NewDecoder(resp.Body).Decode(&out.body)
		responses <- out
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		l.bus.mu.Lock()
		n := len(l.bus.subs)
		l.bus.mu.Unlock()
		if n != 0 {
			break
		}
		if time.Now().After(deadline) {
			l.mu.Unlock()
			t.Fatal("wait request did not subscribe")
		}
		time.Sleep(time.Millisecond)
	}

	l.bus.publish(Event{Session: l.id, Event: agent.Event{Kind: agent.EvDone, StopReason: "old-run"}})
	l.status(false)
	l.busy, l.cancel = false, nil
	oldCancel()
	l.mu.Unlock()

	select {
	case got := <-responses:
		t.Fatalf("wait request consumed the preceding run's idle event: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}

	deadline = time.Now().Add(5 * time.Second)
	for !l.isBusy() {
		if time.Now().After(deadline) {
			t.Fatal("new run did not start")
		}
		time.Sleep(time.Millisecond)
	}
	var cancelled struct{ Cancelled bool }
	if code := h.do(http.MethodPost, "/v1/sessions/"+st.ID+"/cancel", nil, &cancelled); code != http.StatusOK || !cancelled.Cancelled {
		t.Fatalf("cancel status = %d, response = %+v", code, cancelled)
	}
	select {
	case got := <-responses:
		if got.err != nil || got.code != http.StatusOK || got.body.StopReason != "interrupted" {
			t.Fatalf("wait response = %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("wait request did not finish after its run was cancelled")
	}
}

func TestPromptAfterSeqIsRunBoundary(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	l := h.srv.lookup(st.ID)
	prior := l.bus.publish(Event{Session: l.id, Event: agent.Event{Kind: agent.EvNotice, Text: "before"}}).Seq

	var out struct {
		AfterSeq int64 `json:"after_seq"`
	}
	if code := h.do(http.MethodPost, "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "slow"}, &out); code != http.StatusAccepted {
		t.Fatalf("prompt status = %d", code)
	}
	if out.AfterSeq != prior {
		t.Fatalf("after_seq = %d, want run boundary %d", out.AfterSeq, prior)
	}
	h.do(http.MethodPost, "/v1/sessions/"+st.ID+"/cancel", nil, nil)
}
