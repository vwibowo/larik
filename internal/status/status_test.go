package status

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestTrackerFollowsRuns(t *testing.T) {
	tr := NewTracker(nil)
	check := func(what string, want State) {
		t.Helper()
		if got := tr.Report().State; got != want {
			t.Fatalf("%s: state %s, want %s", what, got, want)
		}
	}
	check("new", Idle)
	tr.Busy(true)
	check("running", Working)
	tr.Block("perm-1", Permission, "needs your permission to use bash")
	tr.Block("perm-2", Question, "has a question for you")
	if r := tr.Report(); r.State != Blocked || r.Kind != Permission {
		t.Fatalf("the oldest request shows first: %+v", r)
	}
	tr.Unblock("perm-1")
	if r := tr.Report(); r.Kind != Question {
		t.Fatalf("the next request shows once the first is answered: %+v", r)
	}
	tr.Unblock("perm-2")
	tr.Failed()
	tr.Busy(false)
	check("failed turn", Error)
	tr.Busy(true)
	tr.Stopped("interrupted")
	tr.Busy(false)
	check("interrupted turn", Idle)
	tr.Busy(true)
	tr.Stopped("end_turn")
	tr.Busy(false)
	check("finished turn", Done)
}

func TestStatusFile(t *testing.T) {
	session := filepath.Join(t.TempDir(), "20261102-100000-abcdef.jsonl")
	if _, ok := Read(session); ok {
		t.Fatal("no file yet")
	}
	t.Setenv(TabEnv, "tab-1")
	w := NewWriter(session, "tui")
	w.Set(Report{State: Working})
	f, ok := Read(session)
	if !ok || f.PID != os.Getpid() || f.FrontEnd != "tui" || f.Tab != "tab-1" || !f.Running() || f.Current().State != Working {
		t.Fatalf("after Set: %+v", f)
	}
	if info, err := os.Stat(Path(session)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("status file mode: %v %v", info.Mode(), err)
	}

	// Released while working, the session reads as idle; a finished turn
	// keeps showing.
	w.Release()
	if f, _ := Read(session); f.Running() || f.Current().State != Idle {
		t.Fatalf("released while working: %+v", f)
	}
	w.Set(Report{State: Done})
	w.Release()
	if f, _ := Read(session); f.Current().State != Done {
		t.Fatalf("released when done: %+v", f)
	}

	if NewWriter("", "tui") != nil {
		t.Fatal("no session, no writer")
	}
}

func TestStatusFromExitedProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	f := File{Report: Report{State: Blocked, Kind: Permission}, PID: cmd.Process.Pid}
	if f.Running() || f.Current().State != Idle {
		t.Fatalf("a blocked report from an exited process must read as idle: %+v", f.Current())
	}
	f.State = Error
	if f.Current().State != Error {
		t.Fatal("an error outlives its process")
	}
}

func TestStatusUsage(t *testing.T) {
	session := filepath.Join(t.TempDir(), "s.jsonl")
	w := NewWriter(session, "tui")
	u := Usage{Model: "m", Mode: "auto", CostUSD: 0.31, Tokens: 42_000, Window: 100_000}
	w.SetUsage(u) // nothing to write it with yet
	if _, ok := Read(session); ok {
		t.Fatal("usage alone writes no file")
	}
	w.Set(Report{State: Done})
	f, _ := Read(session)
	if f.Usage != u || f.Usage.ContextPercent() != 42 {
		t.Fatalf("usage: %+v", f.Usage)
	}
	// The usage moving on doesn't move the report's time.
	reported := f.Since()
	time.Sleep(2 * time.Millisecond)
	u.CostUSD = 0.40
	w.SetUsage(u)
	g, _ := Read(session)
	if g.Usage.CostUSD != 0.40 || !g.Since().Equal(reported) || !g.Updated.After(f.Updated) {
		t.Fatalf("after the usage alone: %+v", g)
	}
	if (Usage{Tokens: 5}).ContextPercent() != -1 {
		t.Fatal("no window, no percent")
	}
}
