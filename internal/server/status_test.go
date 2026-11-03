package server

import (
	"path/filepath"
	"testing"
	"time"

	"larik/internal/status"
)

func TestSessionStatusFile(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	path := filepath.Join(h.srv.app.SessionDir, st.ID+".jsonl")
	waitFor := func(what string, ok func(status.File) bool) status.File {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			f, _ := status.Read(path)
			if ok(f) {
				return f
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: status file %+v", what, f)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "hello", "wait": true}, nil)
	f := waitFor("finished turn", func(f status.File) bool { return f.State == status.Done })
	if f.FrontEnd != "serve" || !f.Running() {
		t.Fatalf("finished turn: %+v", f)
	}

	h.do("POST", "/v1/sessions/"+st.ID+"/prompt", map[string]any{"text": "write it"}, nil)
	f = waitFor("permission prompt", func(f status.File) bool { return f.State == status.Blocked })
	if f.Kind != status.Permission || f.Msg != "needs your permission to use write" {
		t.Fatalf("permission prompt: %+v", f)
	}

	h.do("DELETE", "/v1/sessions/"+st.ID, nil, nil)
	f = waitFor("closed session", func(f status.File) bool { return f.PID == 0 })
	if f.Current().State != status.Idle {
		t.Fatalf("a session closed while blocked reads as idle: %+v", f.Current())
	}
}
