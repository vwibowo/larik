package desktop

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

func focusByTab(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	for range 150 {
		if tt.Focused(label) {
			return
		}
		tt.Key(0, ui.KeyTab)
		tt.Frame()
	}
	t.Fatalf("Tab never reached %q", label)
}

func TestNotificationControlsWorkFromKeyboard(t *testing.T) {
	a, _ := attentionApp(t)
	a.record(event{title: "Build", body: "passed"})
	a.record(event{title: "Tests", body: "finished"})
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	focusByTab(t, tt, "2 unread notifications ("+keyLabel(true, "U")+" opens the newest)")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if !a.showEvents {
		t.Fatal("Enter did not open notifications")
	}
	focusByTab(t, tt, "Tests: finished")
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if a.unreadEvents() != 1 {
		t.Fatalf("Space did not open the notification: %d unread", a.unreadEvents())
	}
	focusByTab(t, tt, "Mark All Read")
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.unreadEvents() != 0 {
		t.Fatal("Enter did not mark notifications read")
	}
}

func TestPaneCloseButtonWorksFromKeyboard(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	a.openBrowser("")
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	focusByTab(t, tt, "Close pane")
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if len(a.tabs) != 0 {
		t.Fatalf("Space did not close the pane: %d tabs", len(a.tabs))
	}
}

// attentionApp is a window with two tabs, the first focused, and a func
// that indexes their sessions as the states say.
func attentionApp(t *testing.T) (*App, func(one, two status.State)) {
	a := New(Options{})
	one, two := &tab{id: "t1", dir: "/work", path: "/one.jsonl"}, &tab{id: "t2", dir: "/work", path: "/two.jsonl"}
	a.tabs = []*tab{one, two}
	a.activate(one)
	return a, func(st1, st2 status.State) { indexTwo(a, "/work", st1, st2) }
}

// indexTwo indexes the sessions of a's two tabs, in dir, as the states say.
func indexTwo(a *App, dir string, st1, st2 status.State) {
	var ss []session.Indexed
	for i, st := range []status.State{st1, st2} {
		s := session.Indexed{Status: status.Report{State: st}}
		s.ID, s.Path, s.Cwd, s.Modified = []string{"one", "two"}[i], []string{"/one.jsonl", "/two.jsonl"}[i], dir, time.Now()
		s.Title = []string{"Write the docs", "Fix the build"}[i]
		if st == status.Blocked {
			s.Status.Msg = "needs your permission to use bash"
		}
		ss = append(ss, s)
	}
	a.setProjects([]session.Project{{Root: dir, Name: "work", Sessions: ss}})
}

func TestAttentionWhileFocused(t *testing.T) {
	a, index := attentionApp(t)
	one, two := a.tabs[0], a.tabs[1]
	index(status.Working, status.Working)
	if a.needsLook(two) || len(a.events) != 0 {
		t.Fatal("nothing to look at yet")
	}
	// The other tab's session finishes: its tab rings and the list has it,
	// unread.
	time.Sleep(time.Millisecond)
	index(status.Working, status.Done)
	if !a.needsLook(two) || a.needsLook(one) {
		t.Fatal("the other tab's finished session rings, the focused one doesn't")
	}
	if len(a.events) != 1 || a.events[0].read || a.events[0].title != "Fix the build" || a.events[0].body != "Finished" || a.unreadEvents() != 1 {
		t.Fatalf("events: %+v", a.events)
	}
	// The focused tab's session waiting for you is read already: you're
	// looking at it.
	index(status.Blocked, status.Done)
	if len(a.events) != 2 || !a.events[1].read || a.needsLook(one) {
		t.Fatalf("the focused session's: %+v", a.events)
	}
	// The newest unread opens its session, which reads it and ends the ring.
	a.latestUnread()
	if a.focused() != two || a.needsLook(two) || a.unreadEvents() != 0 {
		t.Fatalf("the latest unread: focused %s, unread %d", a.focused().id, a.unreadEvents())
	}
	// Now the first tab waits for you, while you look at the second.
	if !a.needsLook(one) {
		t.Fatal("a blocked session rings while you look elsewhere")
	}
	a.latestUnread()
	if a.notice != "No unread notifications." {
		t.Fatalf("notice %q", a.notice)
	}
}

func TestEventsKept(t *testing.T) {
	a := New(Options{})
	for i := 0; i < eventsKept+10; i++ {
		a.record(event{title: "n", body: string(rune('a' + i%26))})
	}
	if len(a.events) != eventsKept || a.events[0].body != string(rune('a'+10%26)) {
		t.Fatalf("kept %d, the oldest %q", len(a.events), a.events[0].body)
	}
	a.markAllRead()
	if a.unreadEvents() != 0 {
		t.Fatal("marked all read")
	}
}

func TestNotificationList(t *testing.T) {
	needTerminal(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t)})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	dir := t.TempDir()
	one, two := a.startTab(dir, "", "/one.jsonl"), a.startTab(dir, "", "/two.jsonl")
	a.activate(one)
	indexTwo(a, dir, status.Working, status.Working)
	time.Sleep(time.Millisecond)
	indexTwo(a, dir, status.Working, status.Blocked)
	a.record(event{title: "Build", body: "passed"})

	tt := ui.NewTester(a.view, 1240, 800)
	tt.SetDark(true)
	tt.Frame()
	snapshot(t, tt, "attention-ring-dark.png")
	bell := "2 unread notifications (" + keyLabel(true, "U") + " opens the newest)"
	if err := tt.Click(bell); err != nil {
		t.Fatalf("%v: %q", err, tt.Texts())
	}
	tt.Frame()
	snapshot(t, tt, "notifications-dark.png")
	for _, want := range []string{"Notifications", "Mark All Read", "Fix the build", "Larik needs your permission to use bash", "Build", "passed"} {
		if !tt.HasText(want) {
			t.Fatalf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Fix the build: Larik needs your permission to use bash"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.focused() != two || a.showEvents || a.unreadEvents() != 1 {
		t.Fatalf("opening a notification: focused %s, open %v, unread %d", a.focused().id, a.showEvents, a.unreadEvents())
	}
}
