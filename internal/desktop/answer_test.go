package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

func TestAnswerLabels(t *testing.T) {
	for an, want := range map[answer][2]string{
		allowOnce:   {"Allow", "Approve"},
		allowAlways: {"Always Allow", "Approve, Ask Each Edit"},
		deny:        {"Deny", "Keep Planning"},
	} {
		if an.label(false) != want[0] || an.label(true) != want[1] {
			t.Errorf("%d: %q %q", an, an.label(false), an.label(true))
		}
	}
	plan := status.Report{State: status.Blocked, Kind: status.Permission, Msg: status.PlanReview}
	if !plan.ReviewsPlan() || (status.Report{State: status.Blocked, Kind: status.Permission, Msg: "needs your permission to use bash"}).ReviewsPlan() {
		t.Fatal("a plan review is told from a tool's permission")
	}
}

func TestAnswerInPlace(t *testing.T) {
	needTerminal(t)
	dir := t.TempDir()
	// Stands in for larik's prompt: reads one key, and says which.
	bin := filepath.Join(t.TempDir(), "prompt")
	script := "#!/bin/sh\nstty raw -echo\nk=$(dd bs=1 count=1 2>/dev/null)\nstty sane\necho \"got:$k\"\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	w := status.NewWriter(path, "tui")
	waiting := status.Report{State: status.Blocked, Kind: status.Permission, Msg: "needs your permission to use bash"}
	w.Set(waiting)
	a, mu := serialApp(Options{Bin: bin})
	defer a.Close()
	mu.Lock()
	tb := a.startTab(dir, "", path)
	reindex := func(r status.Report) {
		s := session.Indexed{Status: r, Running: true}
		s.ID, s.Path, s.Cwd, s.Title = "s1", path, dir, "Clean up the build"
		a.setProjects([]session.Project{{Root: dir, Name: "work", Sessions: []session.Indexed{s}}})
	}
	reindex(waiting)
	tt := ui.NewTester(a.view, 1240, 800)
	tt.SetDark(true)
	tt.Frame()
	snapshot(t, tt, "answer-dark.png")
	for _, want := range []string{"Allow", "Always", "Deny"} {
		if !tt.HasText(want) {
			mu.Unlock()
			t.Fatalf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Allow: Clean up the build"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	if a.notice != "" || a.answered[path].sent.IsZero() {
		mu.Unlock()
		t.Fatalf("answering: notice %q", a.notice)
	}
	mu.Unlock()
	frameUntil(t, mu, tt, "the key in the tab", func() bool { return strings.Contains(tb.term.Text(), "got:1") })

	// larik takes it and goes on: nothing to say, and the buttons are
	// back without waiting out the check.
	w.Set(status.Report{State: status.Working})
	time.Sleep(500 * time.Millisecond)
	mu.Lock()
	if a.notice != "" || len(a.answered) != 0 {
		mu.Unlock()
		t.Fatalf("after an answer taken: %q", a.notice)
	}
	// Waiting again, and this time the tab doesn't take it: say so.
	w.Set(waiting)
	if msg := a.answerSession(path, deny); msg != "" {
		mu.Unlock()
		t.Fatal(msg)
	}
	mu.Unlock()
	time.Sleep(answerChecked + 500*time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(a.notice, "didn't take the answer") {
		t.Fatalf("an answer not taken: %q", a.notice)
	}
	// Nothing waiting, nothing typed.
	w.Set(status.Report{State: status.Working})
	if msg := a.answerSession(path, allowOnce); !strings.Contains(msg, "isn't waiting") {
		t.Fatalf("answering what doesn't wait: %q", msg)
	}
	reindex(status.Report{State: status.Working})
	if _, ok := a.answerable(session.Indexed{Status: status.Report{State: status.Working}}); ok {
		t.Fatal("a working session is answerable")
	}
	// A prompt the index hasn't seen yet is answered: its status file
	// says it waits.
	w.Set(waiting)
	if msg := a.answerSession(path, deny); msg != "" {
		t.Fatalf("answering before the index caught up: %q", msg)
	}
}

func TestAllowCommand(t *testing.T) {
	needTerminal(t)
	dir, data := t.TempDir(), t.TempDir()
	bin := filepath.Join(t.TempDir(), "prompt")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	w := status.NewWriter(path, "tui")
	waiting := status.Report{State: status.Blocked, Kind: status.Permission, Msg: "needs your permission to use write"}
	w.Set(waiting)
	a, mu := serialApp(Options{Bin: bin, DataDir: data})
	defer a.Close()
	mu.Lock()
	tb := a.startTab(dir, "", path)
	s := session.Indexed{Status: waiting, Running: true}
	s.ID, s.Path, s.Cwd, s.Title = "abc1", path, dir, "Write the note"
	a.setProjects([]session.Project{{Root: dir, Sessions: []session.Indexed{s}}})
	mu.Unlock()
	ln, err := listen(data, func(string) {}, a.control)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// larik takes it and goes on to work: allow says so once it has.
	go func() {
		time.Sleep(300 * time.Millisecond)
		w.Set(status.Report{State: status.Working})
	}()
	res, err := Call(data, Request{Cmd: "allow", Tab: tb.id})
	if err != nil || res.State != "working" {
		t.Fatalf("allow: %v %+v", err, res)
	}
	// Nothing waits now.
	if _, err := Call(data, Request{Cmd: "deny", Tab: "abc1"}); err == nil || !strings.Contains(err.Error(), "isn't waiting") {
		t.Fatalf("deny with nothing waiting: %v", err)
	}
	// The next prompt, straight away: answered as one of its own, though
	// nothing in the tab takes the key.
	w.Set(waiting)
	if _, err := Call(data, Request{Cmd: "deny", Tab: tb.id}); err == nil || !strings.Contains(err.Error(), "didn't take the answer") {
		t.Fatalf("deny not taken: %v", err)
	}
}
