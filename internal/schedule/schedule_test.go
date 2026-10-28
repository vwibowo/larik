package schedule

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestCronNext(t *testing.T) {
	for _, c := range []struct{ expr, from, want string }{
		{"*/15 * * * *", "2026-10-28 10:07", "2026-10-28 10:15"},
		{"0 9 * * 1-5", "2026-10-30 09:00", "2026-11-02 09:00"}, // Friday 9:00 → Monday
		{"@daily", "2026-12-31 23:59", "2027-01-01 00:00"},
		{"30 8 1 * *", "2026-10-28 00:00", "2026-11-01 08:30"},
		{"0 12 13 * 5", "2026-10-28 00:00", "2026-10-30 12:00"}, // day 13 or a Friday
		{"0 0 * * 7", "2026-10-28 00:00", "2026-11-01 00:00"},   // 7 is Sunday
		{"5/20 * * * *", "2026-10-28 10:26", "2026-10-28 10:45"},
	} {
		cr, err := ParseCron(c.expr)
		if err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := cr.Next(at(c.from)); !got.Equal(at(c.want)) {
			t.Errorf("%s after %s = %s, want %s", c.expr, c.from, got.Format("2006-01-02 15:04"), c.want)
		}
	}
	never, _ := ParseCron("0 0 30 2 *")
	if got := never.Next(at("2026-01-01 00:00")); !got.IsZero() {
		t.Errorf("Feb 30 should never match, got %s", got)
	}
}

func TestCronErrors(t *testing.T) {
	for _, expr := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * 0 * *", "*/0 * * * *", "5-1 * * * *", "a * * * *"} {
		if _, err := ParseCron(expr); err == nil {
			t.Errorf("%q should not parse", expr)
		}
	}
}

func TestStore(t *testing.T) {
	s := Store{Path: filepath.Join(t.TempDir(), "schedules.json")}
	if _, err := s.Add(Entry{Cron: "0 0 30 2 *", Prompt: "x"}); err == nil || !strings.Contains(err.Error(), "never matches") {
		t.Fatalf("an impossible cron should be refused: %v", err)
	}
	if _, err := s.Add(Entry{Cron: "@daily", Prompt: " "}); err == nil {
		t.Fatal("an empty prompt should be refused")
	}
	a, err := s.Add(Entry{Cron: "@daily", Prompt: "check the build", Cwd: "/w"})
	if err != nil || len(a.ID) != 6 {
		t.Fatalf("add: %+v %v", a, err)
	}
	if list, _ := s.List(); len(list) != 1 || list[0].Prompt != "check the build" {
		t.Fatalf("list %+v", list)
	}
	if _, err := s.Remove("zzz"); err == nil {
		t.Fatal("removing an unknown id should fail")
	}
	if e, err := s.Remove(a.ID[:3]); err != nil || e.ID != a.ID {
		t.Fatalf("remove by prefix: %+v %v", e, err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("left %+v", list)
	}
}

func TestDaemonRunsDueEntries(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "fake-larik")
	// Stands in for larik: records its directory and arguments.
	script := "#!/bin/sh\necho \"$PWD\" > ran.txt\nprintf '%s\\n' \"$@\" >> ran.txt\necho output\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	s := Store{Path: filepath.Join(dir, "schedules.json")}
	created := at("2026-10-28 08:00")
	due, _ := s.Add(Entry{Cron: "0 9 * * *", Prompt: "summarize", Cwd: work, Mode: "accept-edits", Timeout: "5m", Created: created})
	later, _ := s.Add(Entry{Cron: "0 18 * * *", Prompt: "later", Cwd: work, Created: created})

	d := &Daemon{Store: s, Exe: exe, LogDir: filepath.Join(dir, "logs"), Now: func() time.Time { return at("2026-10-28 09:30") }, running: map[string]bool{}}
	if err := d.startDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	d.wg.Wait()
	ran, err := os.ReadFile(filepath.Join(work, "ran.txt"))
	if err != nil {
		t.Fatal("the due entry should have run:", err)
	}
	if got := strings.Fields(string(ran)); strings.Join(got[1:], " ") != "-p --mode accept-edits --timeout 5m -- summarize" {
		t.Errorf("args %q", got)
	}
	list, _ := s.List()
	for _, e := range list {
		switch e.ID {
		case due.ID:
			if e.LastStatus != "ok" || !e.LastRun.Equal(at("2026-10-28 09:30")) || !e.Next().Equal(at("2026-10-29 09:00")) {
				t.Errorf("due entry after its run: %+v", e)
			}
			if out, _ := os.ReadFile(e.LastLog); !strings.Contains(string(out), "output") {
				t.Errorf("log %q", out)
			}
		case later.ID:
			if !e.LastRun.IsZero() {
				t.Errorf("an entry not yet due ran: %+v", e)
			}
		}
	}
	// Run again at the same time: nothing is due any more.
	os.Remove(filepath.Join(work, "ran.txt"))
	_ = d.startDue(context.Background())
	d.wg.Wait()
	if _, err := os.Stat(filepath.Join(work, "ran.txt")); err == nil {
		t.Error("an entry ran twice for one time")
	}
}
