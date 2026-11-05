package desktop

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

func TestParseGitStatus(t *testing.T) {
	out := "# branch.oid 0123456789abcdef\n# branch.head feature/x\n# branch.upstream origin/feature/x\n1 .M N... 100644 100644 100644 a b main.go\n? new.txt\n"
	if m := parseGitStatus([]byte(out)); m.Branch != "feature/x" || m.Dirty != 2 {
		t.Fatalf("%+v", m)
	}
	detached := "# branch.oid 0123456789abcdef\n# branch.head (detached)\n"
	if m := parseGitStatus([]byte(detached)); m.Branch != "0123456" || m.Dirty != 0 {
		t.Fatalf("detached: %+v", m)
	}
}

func TestPortsOfSessions(t *testing.T) {
	// 100 is a session's larik, 200 a shell it ran, 300 a server the
	// shell started without a terminal; 500 one started with & whose
	// shell exited, on the session's terminal; 400 is someone else's.
	ps := "    1     0 ??\n  100     1 ttys004\n  200   100 ttys004\n  300   200 ??\n  400     1 ttys001\n  500     1 ttys004\n"
	owner := descendants(parsePS([]byte(ps)), map[string]int{"/s.jsonl": 100})
	if owner[300] != "/s.jsonl" || owner[200] != "/s.jsonl" || owner[500] != "/s.jsonl" || owner[400] != "" || owner[1] != "" {
		t.Fatalf("owners: %v", owner)
	}
	lsof := "p300\nf5\nn*:8000\nf6\nn[::1]:8000\nf7\nn127.0.0.1:5173\np400\nn*:9999\n"
	got := parseLsof([]byte(lsof), owner)
	if !slices.Equal(got["/s.jsonl"], []int{5173, 8000}) || len(got) != 1 {
		t.Fatalf("ports: %v", got)
	}
}

func TestLastLines(t *testing.T) {
	if got := lastLines("a\nb\nc\n\n   \n", 2); got != "b\nc" {
		t.Fatalf("%q", got)
	}
	if got := lastLines("a\nb", 0); got != "a\nb" {
		t.Fatalf("all: %q", got)
	}
}

func TestControlOverSocket(t *testing.T) {
	data := t.TempDir()
	path := filepath.Join(t.TempDir(), "s1.jsonl")
	a := New(Options{DataDir: data})
	var posted string
	a.post = func(title, body string) { posted = title + "|" + body }
	s := session.Indexed{Status: status.Report{State: status.Idle}}
	s.ID, s.Path, s.Cwd, s.Title = "abc123", path, "/work", "Fix the tests"
	a.setProjects([]session.Project{{Root: "/work", Sessions: []session.Indexed{s}}})
	one, two := &tab{id: "t1", dir: "/work", path: path}, &tab{id: "t2", dir: "/other"}
	a.tabs = []*tab{one, two}
	a.activate(one)

	ln, err := listen(data, func(string) {}, a.control)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	res, err := Call(data, Request{Cmd: "list"})
	if err != nil || len(res.Tabs) != 2 {
		t.Fatalf("list: %v %+v", err, res)
	}
	if got := res.Tabs[0]; got.ID != "t1" || got.Session != "abc123" || got.Title != "Fix the tests" || !got.Focused || !got.Shown {
		t.Fatalf("the first tab: %+v", got)
	}
	// A tab by the start of its session's id, or "focused".
	for _, ref := range []string{"abc", "focused", "t1"} {
		if res, err := Call(data, Request{Cmd: "focus", Tab: ref}); err != nil || res.Tab.ID != "t1" {
			t.Fatalf("focus %q: %v %+v", ref, err, res)
		}
	}
	if _, err := Call(data, Request{Cmd: "read"}); err == nil || !strings.Contains(err.Error(), "which tab") {
		t.Fatalf("read without a tab: %v", err)
	}
	if _, err := Call(data, Request{Cmd: "send", Tab: "nope", Text: "hi"}); err == nil {
		t.Fatal("sent to a tab that isn't open")
	}
	if _, err := Call(data, Request{Cmd: "bogus"}); err == nil {
		t.Fatal("an unknown command answered")
	}
	if _, err := Call(data, Request{Cmd: "notify", Title: "Build", Body: "passed"}); err != nil || posted != "Build|passed" || a.notice != "Build: passed" || len(a.events) != 1 || a.events[0].body != "passed" {
		t.Fatalf("notify: %v %q %q", err, posted, a.notice)
	}
	if _, err := Call(data, Request{Cmd: "open", Cwd: filepath.Join(data, "missing")}); err == nil || !strings.Contains(err.Error(), "Can't start larik") {
		t.Fatalf("open in a folder that isn't there: %v", err)
	}
	if res, err := Call(data, Request{Cmd: "close", Tab: "t2"}); err != nil || res.Tab.ID != "t2" || len(a.tabs) != 1 {
		t.Fatalf("close: %v %d", err, len(a.tabs))
	}

	// A done from before the wait doesn't end it; the next one does.
	w := status.NewWriter(path, "tui")
	w.Set(status.Report{State: status.Done})
	go func() {
		time.Sleep(600 * time.Millisecond)
		w.Set(status.Report{State: status.Working})
		time.Sleep(100 * time.Millisecond)
		w.Set(status.Report{State: status.Done, Msg: "finished"})
	}()
	start := time.Now()
	res, err = Call(data, Request{Cmd: "wait", Tab: "t1", Timeout: 5 * time.Second})
	if err != nil || res.State != "done" || time.Since(start) < 600*time.Millisecond {
		t.Fatalf("wait: %v %+v after %s", err, res, time.Since(start))
	}
	// Blocked counts as it is.
	w.Set(status.Report{State: status.Blocked, Kind: status.Permission})
	if res, err := Call(data, Request{Cmd: "wait", Tab: "t1", State: "blocked,error", Timeout: 2 * time.Second}); err != nil || res.State != "blocked" {
		t.Fatalf("wait for blocked: %v %+v", err, res)
	}
	if _, err := Call(data, Request{Cmd: "wait", Tab: "t1", Timeout: 500 * time.Millisecond}); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("a wait that times out: %v", err)
	}
	if _, err := Call(data, Request{Cmd: "wait", Tab: "t1", State: "finished"}); err == nil {
		t.Fatal("waited for a state there isn't")
	}
}

func TestCallWithNoWindow(t *testing.T) {
	if _, err := Call(t.TempDir(), Request{Cmd: "list"}); err != errNotOpen {
		t.Fatalf("%v", err)
	}
}

func TestSidebarDetails(t *testing.T) {
	a := New(Options{})
	var opened string
	a.openExternal = func(url string) { opened = url }
	working := session.Indexed{Status: status.Report{State: status.Working, Msg: "running go test ./..."}}
	working.ID, working.Path, working.Cwd, working.Title = "s1", "/s1.jsonl", "/work", "Fix the flaky test"
	worktree := session.Indexed{Status: status.Report{State: status.Idle}, Subdir: "../work-wt", Running: true}
	worktree.ID, worktree.Path, worktree.Cwd, worktree.Title = "s2", "/s2.jsonl", "/work-wt", "Try the new parser"
	a.setProjects([]session.Project{{Root: "/work", Name: "work", Sessions: []session.Indexed{working, worktree}}})
	a.git = map[string]gitMeta{"/work": {Branch: "main", Dirty: 3}, "/work-wt": {Branch: "parser-v2"}}
	a.ports = map[string][]int{"/s1.jsonl": {5173}}

	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	snapshot(t, tt, "sidebar-details-dark.png")
	for _, want := range []string{"work · main ±3", "running go test ./...", "parser-v2", ":5173"} {
		if !tt.HasText(want) {
			t.Fatalf("no %q in %q", want, tt.Texts())
		}
	}
	if err := tt.Click("Open http://localhost:5173"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if opened != "" || len(a.tabs) != 1 || !a.tabs[0].browser || a.tabs[0].url != "http://localhost:5173" {
		t.Fatalf("the port opened %q, tabs %d", opened, len(a.tabs))
	}
	// Again, it shows the tab already on it; Cmd-clicked, your browser.
	a.openPort("/s1.jsonl", 5173, false)
	if len(a.tabs) != 1 {
		t.Fatalf("a second tab on the port: %d", len(a.tabs))
	}
	a.openPort("/s1.jsonl", 5173, true)
	if opened != "http://localhost:5173" {
		t.Fatalf("Cmd-click opened %q", opened)
	}
}

func TestBackgroundOpenKeepsFocusAndShowsPane(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	a.openBrowser("https://example.com")
	first := a.focused()
	raised := 0
	a.raise = func() { raised++ }
	res := a.controlOpen(Request{Cmd: "open", URL: "https://example.org", Background: true})
	if res.Error != "" || res.Tab == nil || !res.Tab.Shown || len(a.panes()) != 2 {
		t.Fatalf("background open should tile a pane: %+v, %d panes", res, len(a.panes()))
	}
	if a.focused() != first {
		t.Fatal("background open changed focus")
	}
	if raised != 0 {
		t.Fatal("background open raised its window")
	}
}
