package desktop

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestDetachTerminalSurvivesWindowAndReattaches(t *testing.T) {
	needTerminal(t)
	data, dir := fixture(t)
	h := testHost(t, data)
	opts := Options{DataDir: data, Cwd: dir, Host: true}
	a, mu := serialApp(opts)
	defer a.Close()
	mu.Lock()
	tb := a.startShell(dir, "")
	if tb == nil {
		mu.Unlock()
		t.Fatal("could not open hosted shell")
	}
	a.activate(tb)
	id := tb.id
	if !a.canDetach(tb) {
		mu.Unlock()
		t.Fatal("hosted shell cannot detach")
	}
	tt := ui.NewTester(a.view, 1240, 800)
	if err := tt.Click("Detach pane (keep terminal running)"); err != nil {
		mu.Unlock()
		t.Fatal(err)
	}
	mu.Unlock()
	frameUntil(t, mu, tt, "terminal detached", func() bool { return a.tabByID(id) == nil })
	if list := h.list(); len(list) != 1 || !list[0].Detached || list[0].Exited {
		t.Fatalf("host did not retain live terminal: %+v", list)
	}
	conn, _, _, err := askHost(data, hostRequest{Op: "send", ID: id, Text: "echo DETACHED_ALIVE", Enter: true})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		conn, _, rep, err := askHost(data, hostRequest{Op: "read", ID: id})
		if conn != nil {
			conn.Close()
		}
		if err == nil && strings.Contains(rep.Text, "DETACHED_ALIVE") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached terminal did not accept input: %v %q", err, rep.Text)
		}
		time.Sleep(10 * time.Millisecond)
	}
	a.Close()
	b, mu2 := serialApp(opts)
	defer b.Close()
	mu2.Lock()
	if b.restore() || len(b.tabs) != 0 {
		mu2.Unlock()
		t.Fatal("detached terminal was restored into a pane")
	}
	b.detached = b.readDetached()
	if len(b.detached) != 1 || b.detached[0].ID != id {
		mu2.Unlock()
		t.Fatalf("detached terminal missing after restart: %+v", b.detached)
	}
	uiTest := ui.NewTester(b.view, 1240, 800)
	uiTest.Frame()
	if !uiTest.HasText("Detached Terminals") {
		mu2.Unlock()
		t.Fatal("detached section not shown")
	}
	b.openDetached(b.detached[0])
	if len(b.tabs) != 1 || b.tabs[0].id != id || b.paneOf(b.tabs[0]) == nil {
		mu2.Unlock()
		t.Fatalf("reattach did not restore pane and ID: %+v", b.tabs)
	}
	mu2.Unlock()
	frameUntil(t, mu2, uiTest, "same terminal screen", func() bool {
		return strings.Contains(b.tabs[0].term.Text(), "DETACHED_ALIVE")
	})
	if h.list()[0].Detached {
		t.Fatal("host still marks attached terminal detached")
	}
}

func TestDetachedTerminalSharedAcrossWindows(t *testing.T) {
	needTerminal(t)
	data, dir := fixture(t)
	testHost(t, data)
	opts := Options{DataDir: data, Cwd: dir, Host: true}
	a, mainMu := serialApp(opts)
	defer a.Close()
	b, otherMu := serialApp(opts)
	defer b.Close()
	g := newWindowGroup(a)
	g.ids = []string{"other"}
	g.children["other"] = &windowEntry{app: b}
	b.detachedChanged = g.refreshDetached
	b.openDetachedInGroup = func(d detachedTerminal) { g.openDetached(b, d) }
	go b.indexLoop()
	mainMu.Lock()
	tb := a.startShell(dir, "sleep 30")
	if tb == nil {
		mainMu.Unlock()
		t.Fatal("could not start terminal")
	}
	a.activate(tb)
	a.detachPane(tb)
	mainMu.Unlock()
	otherMu.Lock()
	tt := ui.NewTester(b.view, 1240, 800)
	otherMu.Unlock()
	frameUntil(t, otherMu, tt, "other window sees detached terminal", func() bool {
		return len(b.detached) == 1 && b.detached[0].ID == tb.id
	})
	mainMu.Lock()
	mainView := ui.NewTester(a.view, 1240, 800)
	mainMu.Unlock()
	frameUntil(t, mainMu, mainView, "original pane detached", func() bool { return a.tabByID(tb.id) == nil })
	otherMu.Lock()
	b.openDetached(b.detached[0])
	if len(b.tabs) != 1 || b.tabs[0].id != tb.id || g.active != b {
		otherMu.Unlock()
		t.Fatal("other window did not reattach the detached terminal")
	}
	otherMu.Unlock()
	mainMu.Lock()
	if len(a.tabs) != 0 {
		mainMu.Unlock()
		t.Fatal("original window adopted the detached terminal")
	}
	mainMu.Unlock()
}

func TestExitedDetachedTerminalWaitsForEnd(t *testing.T) {
	needTerminal(t)
	h := newHost()
	defer h.closeAll()
	tb, err := h.tab(hostRequest{Op: "open", ID: "finished", Dir: t.TempDir(), Shell: true, Command: []string{"/bin/sh", "-c", "printf final-output"}})
	if err != nil {
		t.Fatal(err)
	}
	<-tb.term.Done()
	h.mu.Lock()
	tb.detached = true
	tb.ended = time.Now().Add(-hostKeepExited - time.Minute)
	h.expireTabs(time.Now())
	h.mu.Unlock()
	if list := h.list(); len(list) != 1 || !list[0].Exited || !list[0].Detached || !strings.Contains(tb.term.Text(), "final-output") {
		t.Fatalf("finished detached terminal was lost: %+v", list)
	}
	h.closeTab(tb.id)
	if len(h.list()) != 0 {
		t.Fatal("End Terminal did not remove host tab")
	}
}

func TestEndRunningDetachedTerminalStopsProcess(t *testing.T) {
	needTerminal(t)
	h := newHost()
	defer h.closeAll()
	tb, err := h.tab(hostRequest{Op: "open", ID: "running", Dir: t.TempDir(), Shell: true, Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	tb.detached = true
	h.mu.Unlock()
	h.closeTab(tb.id)
	select {
	case <-tb.term.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("End Terminal did not stop the running process")
	}
	if len(h.list()) != 0 {
		t.Fatal("ended terminal remained in host")
	}
}

func TestDetachMenuAvailability(t *testing.T) {
	fileHasDetach := func(a *App) bool {
		for _, group := range a.menuDefaults() {
			if group.title == "File" {
				return slices.ContainsFunc(group.items, func(c command) bool { return c.label == "Detach Pane" })
			}
		}
		return false
	}
	a := New(Options{Host: true})
	terminalTab := &tab{id: "terminal", dir: t.TempDir(), cmd: []string{"sh"}, hosted: true}
	a.tabs = []*tab{terminalTab}
	a.activate(terminalTab)
	if !fileHasDetach(a) {
		t.Fatal("hosted terminal missing Detach Pane in File menu")
	}
	terminalTab.hosted = false
	if fileHasDetach(a) {
		t.Fatal("unhosted terminal offered Detach Pane")
	}
	a.activate(&tab{id: "browser", browser: true})
	if fileHasDetach(a) {
		t.Fatal("browser pane offered Detach Pane")
	}
}

func TestRemoteTerminalDetachAndOlderHost(t *testing.T) {
	needTerminal(t)
	remote, dir := fixture(t)
	h, connect := standIn(t, remote)
	dial, bin, _, _, err := connect("devbox")
	if err != nil {
		t.Fatal(err)
	}
	a, mu := serialApp(Options{DataDir: t.TempDir(), Cwd: dir})
	defer a.Close()
	mu.Lock()
	srv := &server{name: "devbox", dial: dial, bin: bin}
	a.servers = []*server{srv}
	tb := a.startShell(remotePath("devbox", dir), "sleep 30")
	if tb == nil {
		mu.Unlock()
		t.Fatal("could not start remote terminal")
	}
	a.activate(tb)
	if a.canDetach(tb) {
		mu.Unlock()
		t.Fatal("old remote host offered Detach Pane")
	}
	a.detachPane(tb)
	if !strings.Contains(a.notice, "update Larik") || a.tabByID(tb.id) == nil {
		mu.Unlock()
		t.Fatal("old remote host did not keep pane with an explanation")
	}
	srv.features = []string{"detached-terminals"}
	a.detachPane(tb)
	mu.Unlock()
	mu.Lock()
	tt := ui.NewTester(a.view, 1240, 800)
	mu.Unlock()
	frameUntil(t, mu, tt, "remote terminal detached", func() bool { return a.tabByID(tb.id) == nil })
	mu.Lock()
	a.adoptRemote(srv)
	if len(a.tabs) != 0 || !h.list()[0].Detached {
		mu.Unlock()
		t.Fatal("remote host auto-adopted detached terminal")
	}
	d := a.readDetached()
	if len(d) != 1 || d[0].source != "devbox" {
		mu.Unlock()
		t.Fatalf("remote detached terminal missing: %+v", d)
	}
	a.serversMu.Lock()
	srv.dial = nil
	a.serversMu.Unlock()
	a.reattachDetached(d[0])
	if !strings.Contains(a.notice, "Connect to devbox") || len(a.tabs) != 0 {
		mu.Unlock()
		t.Fatal("disconnected remote terminal attached")
	}
	a.serversMu.Lock()
	srv.dial = dial
	a.serversMu.Unlock()
	a.reattachDetached(d[0])
	if len(a.tabs) != 1 || a.tabs[0].id != tb.id {
		mu.Unlock()
		t.Fatal("remote terminal did not reattach with its ID")
	}
	mu.Unlock()
	frameUntil(t, mu, tt, "remote host cleared detached flag", func() bool { return !h.list()[0].Detached })
}
