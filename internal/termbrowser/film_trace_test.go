package termbrowser

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/trace"
	traceview "larik/internal/trace/viewer"

	"larik/internal/tools"
)

// A call's picture shows in the real trace page, and a call without one
// leaves no empty space.
func TestTraceShowsThePageAfterACall(t *testing.T) {
	s, srv := newSession(t)
	open(t, s, srv, "/")
	ctx := tools.WithCallID(context.Background(), "call-1")
	input, _ := json.Marshal(map[string]string{"url": srv.URL + "/done"})
	if res := (noted{NavigateTool{s}, s}).Run(ctx, nil, input); res.IsError {
		t.Fatal(res.Content)
	}
	if s.FrameOf("call-1") == nil {
		t.Fatal("no picture for the call")
	}

	dir := filepath.Join(t.TempDir(), "s.trace")
	rec, err := trace.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tr := rec.Tracer()
	tr.TurnStart("look", "", nil)
	tr.ToolStart("call-1", "browser_navigate", input)
	tr.ToolEnd("call-1", "browser_navigate", "ok", false)
	tr.ToolStart("call-2", "read", []byte(`{}`))
	tr.ToolEnd("call-2", "read", "ok", false)
	rec.Close()
	v, err := traceview.Start(traceview.Options{Dir: dir, Title: "s", Frame: s.FrameOf}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if _, err := s.Navigate(context.Background(), v.URL()); err != nil {
		t.Fatal(err)
	}
	show := func(id string) string {
		out, err := s.Eval(context.Background(), `(async () => {
			for (let i = 0; i < 50 && !S.M?.tools?.has(`+"`"+id+"`"+`); i++) await new Promise(r => setTimeout(r, 100));
			select({type: 'tool', id: `+"`"+id+"`"+`});
			await new Promise(r => setTimeout(r, 500));
			const i = document.querySelector('img.frame');
			return i ? 'shown ' + i.naturalWidth : 'none';
		})()`)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := show("call-1"); !strings.Contains(got, "shown") || strings.Contains(got, "shown 0") {
		t.Fatalf("call-1: %s", got)
	}
	if got := show("call-2"); !strings.Contains(got, "none") {
		t.Fatalf("call-2: %s", got)
	}
}
