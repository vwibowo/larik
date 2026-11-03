package desktop

import (
	"encoding/json"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/llm"
	"larik/internal/session"
	"larik/internal/status"
)

// fixture writes sessions for two projects into a data directory and
// returns it, with the first project's directory. One session is blocked
// in a process that is still running: this test's.
func fixture(t *testing.T) (data, repo string) {
	t.Helper()
	base := t.TempDir()
	data = filepath.Join(base, "data")
	repo = filepath.Join(base, "larik-hardness")
	site := filepath.Join(base, "ornith-page")
	for _, d := range []string{filepath.Join(repo, ".git"), site} {
		os.MkdirAll(d, 0o755)
	}
	write := func(cwd string, age time.Duration, msgs ...llm.Message) string {
		s, err := session.Create(session.Dir(data, cwd), session.Meta{Cwd: cwd})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range msgs {
			s.AppendMessage(m, nil)
		}
		s.Close()
		at := time.Now().Add(-age)
		os.Chtimes(s.Path, at, at)
		return s.Path
	}
	write(site, 3*time.Hour, llm.UserText("Polish the landing copy"))
	blocked := write(repo, time.Hour, llm.UserText("Add OSC 7501 output"))
	status.NewWriter(blocked, "tui").Set(status.Report{State: status.Blocked, Kind: status.Permission, Msg: "needs your permission to use bash"})
	write(repo, time.Minute,
		llm.UserText("Refactor the parser and run the tests"),
		llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"go test ./internal/parser"}`)},
		}},
		llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "t1", Content: "ok"}}},
	)
	return data, repo
}

// serialApp is an App whose updates run one at a time, as on a
// main thread, so a test can read its state under mu.
func serialApp(opts Options) (*App, *sync.Mutex) {
	a := New(opts)
	// These run larik in their tabs: a new tab starts it, as with the
	// chooser turned off (chooser_test.go tests the chooser).
	a.noChooser = true
	var mu sync.Mutex
	// As the window's Update, it runs fn later, on the window's turn:
	// a terminal closing calls back while the caller holds the lock.
	a.update = func(fn func()) {
		go func() { mu.Lock(); defer mu.Unlock(); fn() }()
	}
	return a, &mu
}

func index(t *testing.T, data string) []session.Project {
	t.Helper()
	projects, err := session.Index(data, session.IndexOptions{Scratch: true})
	if err != nil {
		t.Fatal(err)
	}
	return projects
}

func TestSidebarGroupsSessionsByProject(t *testing.T) {
	data, _ := fixture(t)
	a, _ := serialApp(Options{DataDir: data})
	defer a.Close()
	a.setProjects(index(t, data))

	tt := ui.NewTester(a.view, 1240, 800)
	tt.SetDark(true)
	tt.Frame()
	for _, want := range []string{
		"larik-hardness", "ornith-page", // projects
		"Add OSC 7501 output", "Refactor the parser and run the tests", // sessions
		"Pick a session in the sidebar, or open a new tab", "Larik Session", "Browser",
		"Inbox",
	} {
		if !tt.HasText(want) {
			t.Errorf("window lacks %q; texts: %q", want, tt.Texts())
		}
	}
	snapshot(t, tt, "window-empty-dark.png")

	// A session open in another terminal stays there.
	if err := tt.Click("Add OSC 7501 output"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.tabs) != 0 || !strings.Contains(a.notice, "open in another terminal") {
		t.Fatalf("tabs %d, notice %q", len(a.tabs), a.notice)
	}
}

// fakeLarik writes a script that stands in for larik: it prints its
// arguments, tab and directory, then exits with the status typed into it.
func fakeLarik(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "larik")
	script := "#!/bin/sh\necho \"args:$* tab:$" + status.TabEnv + " dir:$(pwd)\"\nread code\nexit $code\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// needTerminal skips unless Ghostty's terminal library is at hand, so the
// tests never download it.
func needTerminal(t *testing.T) {
	t.Helper()
	if os.Getenv("MYGO_GHOSTTY_VT") != "" {
		return
	}
	if cache, err := os.UserCacheDir(); err == nil {
		libs, _ := filepath.Glob(filepath.Join(cache, "mygo", "natives", "*", "*ghostty-vt*"))
		if len(libs) > 0 {
			t.Setenv("MYGO_GHOSTTY_VT", libs[0])
			return
		}
	}
	t.Skip("no libghostty-vt in the cache; run larik desktop once to fetch it")
}

func TestTabsRunLarik(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo})
	defer a.Close()

	mu.Lock()
	a.setProjects(index(t, data))
	a.selectStart() // the newest session of repo, which nothing has open
	if len(a.tabs) != 1 || shownAt(a, 0) == nil {
		mu.Unlock()
		t.Fatalf("selectStart opened %d tabs: %s", len(a.tabs), a.notice)
	}
	resumed := shownAt(a, 0)
	s, _, _ := a.find(resumed.path)
	tt := ui.NewTester(a.view, 1240, 800)
	tt.SetDark(true)
	mu.Unlock()

	frameUntil(t, mu, tt, "larik resumes the session", func() bool {
		return strings.Contains(resumed.term.Text(), "args:--resume "+s.ID+" tab:"+resumed.id)
	})
	mu.Lock()
	if !strings.Contains(resumed.term.Text(), filepath.Base(repo)) {
		t.Errorf("larik runs in the session's directory: %q", resumed.term.Text())
	}
	if !tt.HasText("Refactor the parser and run the tests") {
		t.Errorf("the tab and header name the session: %q", tt.Texts())
	}
	snapshot(t, tt, "window-dark.png")

	// The New menu starts larik in the same directory, without
	// --resume; once its status file names the tab, the tab has the
	// session.
	if err := tt.Click("New tab"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("New Larik Session"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	fresh := shownAt(a, 0)
	if len(a.tabs) != 2 || fresh == resumed || fresh.dir != repo {
		mu.Unlock()
		t.Fatalf("new tab: %d tabs, dir %q", len(a.tabs), fresh.dir)
	}
	mu.Unlock()
	frameUntil(t, mu, tt, "larik starts fresh", func() bool {
		return strings.Contains(fresh.term.Text(), "args: tab:"+fresh.id)
	})

	created, err := session.Create(session.Dir(data, repo), session.Meta{Cwd: repo})
	if err != nil {
		t.Fatal(err)
	}
	created.AppendMessage(llm.UserText("Write the changelog"), nil)
	created.Close()
	t.Setenv(status.TabEnv, fresh.id)
	status.NewWriter(created.Path, "tui").Set(status.Report{State: status.Working})
	mu.Lock()
	a.setProjects(index(t, data))
	if fresh.path != created.Path || a.tabTitle(fresh) != "Write the changelog" || a.tabStatus(fresh).State != status.Working {
		t.Errorf("the new tab follows its session: %q %q %+v", fresh.path, a.tabTitle(fresh), a.tabStatus(fresh))
	}
	// Choosing a session that has a tab shows that tab.
	a.selectSession(resumed.path)
	if shownAt(a, 0) != resumed || len(a.tabs) != 2 {
		t.Errorf("the session's tab shows again")
	}
	mu.Unlock()

	// A failed larik keeps its tab, as an error; a clean exit closes it.
	fresh.term.Send([]byte("3\r"))
	waitFor(t, mu, "failed tab", func() bool { return fresh.exited })
	mu.Lock()
	if len(a.tabs) != 2 || a.tabStatus(fresh).State != status.Error {
		t.Errorf("failed larik: %d tabs, %+v", len(a.tabs), a.tabStatus(fresh))
	}
	mu.Unlock()
	resumed.term.Send([]byte("0\r"))
	waitFor(t, mu, "tab closed", func() bool { return len(a.tabs) == 1 })
	mu.Lock()
	defer mu.Unlock()
	if shownAt(a, 0) != fresh {
		t.Errorf("the remaining tab shows")
	}
}

func TestCycleTabs(t *testing.T) {
	a := New(Options{})
	a.tabs = []*tab{{id: "a"}, {id: "b"}, {id: "c"}}
	a.activate(a.tabs[0])
	a.cycle(-1)
	if shownAt(a, 0).id != "c" {
		t.Fatalf("back from the first: %s", shownAt(a, 0).id)
	}
	a.cycle(1)
	a.cycle(1)
	if shownAt(a, 0).id != "b" {
		t.Fatalf("on twice: %s", shownAt(a, 0).id)
	}
}

// snapshot saves the tester's frame where LARIK_DESKTOP_SNAPSHOTS says,
// for looking at the window without opening it.
func snapshot(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("LARIK_DESKTOP_SNAPSHOTS")
	if dir == "" {
		return
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}

// frameUntil draws frames, which start the terminal and show what it
// prints, until ok holds.
func frameUntil(t *testing.T, mu *sync.Mutex, tt *ui.Tester, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		tt.Frame()
		done := ok()
		mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitFor(t *testing.T, mu *sync.Mutex, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		done := ok()
		mu.Unlock()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// shownAt is the tab the i'th pane shows, left to right and top to
// bottom, or nil.
func shownAt(a *App, i int) *tab {
	if ps := a.panes(); i < len(ps) {
		return ps[i].tab
	}
	return nil
}
