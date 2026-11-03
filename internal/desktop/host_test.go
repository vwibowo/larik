package desktop

import (
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestHostMessages(t *testing.T) {
	var b bytes.Buffer
	writeMsg(&b, msgFrame, []byte("\x1b[2Jhello"))
	writeMsg(&b, msgBell, nil)
	for _, want := range []struct {
		kind byte
		data string
	}{{msgFrame, "\x1b[2Jhello"}, {msgBell, ""}} {
		kind, data, err := readMsg(&b)
		if err != nil || kind != want.kind || string(data) != want.data {
			t.Fatalf("%c %q %v, want %c %q", kind, data, err, want.kind, want.data)
		}
	}
	// A length past the bound is refused, not allocated.
	if _, _, err := readMsg(bytes.NewReader([]byte{msgFrame, 0xff, 0xff, 0xff, 0xff})); err == nil {
		t.Fatal("a 4 GB message read")
	}
}

// testHost runs a host for data in the test, as the window would start
// one, ending its programs at the end.
func testHost(t *testing.T, data string) *host {
	t.Helper()
	path := hostSocketPath(data)
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	h := newHost()
	go h.serve(ln)
	t.Cleanup(func() {
		ln.Close()
		h.closeAll()
		os.Remove(path)
	})
	return h
}

// A tab's larik runs in the host, goes on when the window closes, and
// the window opening again attaches to it, showing what it showed; how it
// ends comes back from the host, and closing the tab ends it there.
func TestHostedTabOutlivesTheWindow(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	testHost(t, data)
	opts := Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo, Host: true}

	a, mu := serialApp(opts)
	mu.Lock()
	a.setProjects(index(t, data))
	a.restore() // nothing saved yet
	a.selectStart()
	if len(a.tabs) != 1 || !a.tabs[0].hosted {
		mu.Unlock()
		a.Close()
		t.Fatalf("selectStart: %d tabs: %s", len(a.tabs), a.notice)
	}
	first := a.tabs[0]
	tt := ui.NewTester(a.view, 1200, 800)
	tt.SetDark(true)
	mu.Unlock()
	frameUntil(t, mu, tt, "larik in the host", func() bool {
		return strings.Contains(first.term.Text(), "args:--resume") && strings.Contains(first.term.Text(), "tab:"+first.id)
	})
	// Typed, it goes to the host's terminal, which echoes it back.
	mu.Lock()
	first.term.Send([]byte("47"))
	mu.Unlock()
	frameUntil(t, mu, tt, "the echo", func() bool { return strings.Contains(first.term.Text(), "\n47") })
	// Closing, the window saves which tabs the host runs.
	a.Close()
	var st savedState
	saved, _ := os.ReadFile(a.statePath())
	if json.Unmarshal(saved, &st) != nil || st.Hosted[0] != first.id {
		t.Fatalf("saved: %s", saved)
	}

	// The window's gone; the program isn't.
	tabs, err := hostTabs(data)
	if err != nil || len(tabs) != 1 || tabs[0].ID != first.id || tabs[0].Exited {
		t.Fatalf("the host's tabs after the window closed: %+v, %v", tabs, err)
	}

	b, mu := serialApp(opts)
	defer b.Close()
	mu.Lock()
	b.setProjects(index(t, data))
	if !b.restore() || len(b.tabs) != 1 {
		mu.Unlock()
		t.Fatalf("restore: %d tabs: %s", len(b.tabs), b.notice)
	}
	again := b.tabs[0]
	if again.id != first.id || !again.hosted || again.path != first.path || b.focused() != again {
		mu.Unlock()
		t.Fatalf("restored %+v, want the tab %s attached", again, first.id)
	}
	tt = ui.NewTester(b.view, 1200, 800)
	tt.SetDark(true)
	mu.Unlock()
	frameUntil(t, mu, tt, "what it showed", func() bool {
		text := again.term.Text()
		return strings.Contains(text, "tab:"+first.id) && strings.Contains(text, "\n47")
	})
	mu.Lock()
	if strings.Count(again.term.Text(), "args:") != 1 {
		mu.Unlock()
		t.Fatalf("larik ran again: %q", again.term.Text())
	}
	snapshot(t, tt, "hosted-dark.png")
	// It ends with 47, which the host tells: the tab stays, for what it
	// printed.
	again.term.Send([]byte("\r"))
	mu.Unlock()
	waitFor(t, mu, "the exit", func() bool { return again.exited })
	mu.Lock()
	if again.code != 47 || len(b.tabs) != 1 {
		mu.Unlock()
		t.Fatalf("exited with %d, %d tabs", again.code, len(b.tabs))
	}
	// Closing the tab has the host forget it.
	b.closeTab(again)
	mu.Unlock()
	waitFor(t, mu, "the host forgetting it", func() bool {
		tabs, err := hostTabs(data)
		return err == nil && len(tabs) == 0
	})
}

// A tab the host can't attach to, as one whose program ended while no
// window showed it, resumes its session instead.
func TestHostedTabGoneResumes(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	testHost(t, data)
	projects := index(t, data)
	path := projects[0].Sessions[0].Path
	saved, _ := json.Marshal(savedState{Tabs: []string{path}, Hosted: map[int]string{0: "gone"}})
	os.MkdirAll(filepath.Join(data, "desktop"), 0o700)
	os.WriteFile(filepath.Join(data, "desktop", "state.json"), saved, 0o600)

	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo, Host: true})
	defer a.Close()
	mu.Lock()
	defer mu.Unlock()
	a.setProjects(projects)
	if !a.restore() || len(a.tabs) != 1 || a.tabs[0].id == "gone" || !a.tabs[0].hosted || a.tabs[0].path != path {
		t.Fatalf("restored %d tabs: %s", len(a.tabs), a.notice)
	}
}

// The tabs the host runs that a window didn't save, as one killed before
// it could, come back all the same; quitting and ending them ends them.
func TestHostedTabsAdopted(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	testHost(t, data)
	opts := Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo, Host: true}

	a, mu := serialApp(opts) // never restored, so never saving
	mu.Lock()
	a.setProjects(index(t, data))
	a.selectStart()
	shell := a.startShell(repo, "echo running; sleep 100")
	if len(a.tabs) != 2 || shell == nil || !shell.hosted {
		mu.Unlock()
		a.Close()
		t.Fatalf("%d tabs: %s", len(a.tabs), a.notice)
	}
	ids := []string{a.tabs[0].id, shell.id}
	mu.Unlock()
	a.Close()
	if _, err := os.Stat(a.statePath()); err == nil {
		t.Fatal("a window that never restored saved")
	}

	b, mu := serialApp(opts)
	mu.Lock()
	b.setProjects(index(t, data))
	if !b.restore() || len(b.tabs) != 2 || b.tabs[0].id != ids[0] || b.tabs[1].id != ids[1] {
		mu.Unlock()
		b.Close()
		t.Fatalf("adopted %d tabs: %s", len(b.tabs), b.notice)
	}
	if b.tabs[0].shell() || !b.tabs[1].shell() || b.tabs[1].run != "echo running; sleep 100" || b.focused() != b.tabs[0] {
		mu.Unlock()
		b.Close()
		t.Fatalf("adopted as %+v, %+v", b.tabs[0], b.tabs[1])
	}
	// Quit and End Tabs: nothing working, so it quits at once, and the
	// host's programs end as the window does.
	var quit bool
	b.leave = func(q bool) { quit = q }
	if !slices.ContainsFunc(b.menuBar()[0].items, func(c command) bool { return c.label == "Quit and End Tabs" }) {
		mu.Unlock()
		b.Close()
		t.Fatal("no Quit and End Tabs")
	}
	b.quitEnding()
	mu.Unlock()
	if !quit || !b.ending {
		b.Close()
		t.Fatalf("quit %v, ending %v", quit, b.ending)
	}
	b.Close()
	if tabs, err := hostTabs(data); err != nil || len(tabs) != 0 {
		t.Fatalf("the host still runs %+v, %v", tabs, err)
	}
}
