package server

import (
	"net/http"
	"testing"
	"time"
)

func TestResumeConflictsWhileSessionIsClosing(t *testing.T) {
	h := newHarness(t)
	st := h.create(map[string]any{})
	l := h.srv.lookup(st.ID)

	// Keep close blocked after it installs the closing marker.
	l.runs.Add(1)
	closed := make(chan int, 1)
	go func() {
		req, _ := http.NewRequest(http.MethodDelete, h.url+"/v1/sessions/"+st.ID, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			closed <- 0
			return
		}
		resp.Body.Close()
		closed <- resp.StatusCode
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		h.srv.mu.Lock()
		closing := h.srv.closing[st.ID]
		h.srv.mu.Unlock()
		if closing {
			break
		}
		if time.Now().After(deadline) {
			l.runs.Done()
			t.Fatal("close did not install its marker")
		}
		time.Sleep(time.Millisecond)
	}

	code := h.do(http.MethodPost, "/v1/sessions", map[string]any{"resume": st.ID}, nil)
	l.runs.Done()
	if code != http.StatusConflict {
		t.Fatalf("resume while closing status = %d, want 409", code)
	}
	select {
	case code := <-closed:
		if code != http.StatusNoContent {
			t.Fatalf("close status = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish")
	}
}
