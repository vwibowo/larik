package desktop

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"larik/internal/status"
)

func TestHostForwardsProgramStatus(t *testing.T) {
	needTerminal(t)
	h := newHost()
	defer h.closeAll()
	tb, err := h.tab(hostRequest{
		Op: "open", ID: "status", Shell: true, Dir: t.TempDir(),
		Command: []string{"/bin/sh", "-c", "printf '\\033]7501;state=blocked:kind=question:msg=QW5zd2VyIG1l\\007'; sleep 5"},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		report := tb.records.current()
		h.mu.Unlock()
		if report.State == status.Blocked && report.Kind == status.Question && report.Msg == "Answer me" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("host did not parse OSC 7501: %+v", report)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := h.list()[0].Status.State; got != status.Blocked {
		t.Fatalf("list status: %s", got)
	}
	client, server := net.Pipe()
	defer client.Close()
	go h.attach(tb, server, bufio.NewReader(server))
	r := bufio.NewReader(client)
	client.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		kind, data, err := readMsg(r)
		if err != nil {
			t.Fatal(err)
		}
		if kind != msgStatus {
			continue
		}
		var report status.Report
		if err := json.Unmarshal(data, &report); err != nil || report.State != status.Blocked {
			t.Fatalf("attach status: %+v, %v", report, err)
		}
		break
	}
}

func TestHostStatusRequestSurvivesWindowReattach(t *testing.T) {
	needTerminal(t)
	h := newHost()
	defer h.closeAll()
	tb, err := h.tab(hostRequest{Op: "open", ID: "external", Shell: true, Dir: t.TempDir(), Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := statusPayload(Request{StatusID: "opencode", State: "blocked", Kind: "permission", Title: "OpenCode"})
	if err != nil {
		t.Fatal(err)
	}
	client, server := net.Pipe()
	go h.handle(server)
	if err := json.NewEncoder(client).Encode(hostRequest{Op: "status", ID: tb.id, Status: payload}); err != nil {
		t.Fatal(err)
	}
	var rep hostReply
	if err := json.NewDecoder(client).Decode(&rep); err != nil || rep.Error != "" {
		t.Fatalf("report: %+v %v", rep, err)
	}
	client.Close()
	if got := h.list()[0].Status; got.State != status.Blocked || got.Title != "OpenCode" {
		t.Fatalf("host lost status: %+v", got)
	}
	view, hostSide := net.Pipe()
	defer view.Close()
	go h.attach(tb, hostSide, bufio.NewReader(hostSide))
	view.SetReadDeadline(time.Now().Add(3 * time.Second))
	r := bufio.NewReader(view)
	for {
		kind, data, err := readMsg(r)
		if err != nil {
			t.Fatal(err)
		}
		if kind == msgStatus {
			var got status.Report
			if err := json.Unmarshal(data, &got); err != nil || got.State != status.Blocked {
				t.Fatalf("reattach: %+v %v", got, err)
			}
			break
		}
	}
}

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

// Concurrent opens for one ID must attach to one process, rather than
// starting two and closing the first out from under its window.
func TestConcurrentHostOpenSameID(t *testing.T) {
	needTerminal(t)
	h := newHost()
	req := hostRequest{Op: "open", ID: "same", Command: []string{"sleep", "100"}, Dir: t.TempDir()}
	var wg sync.WaitGroup
	results := make([]*hostTab, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = h.tab(req)
		}()
	}
	wg.Wait()
	defer h.closeAll()
	if errs[0] != nil || errs[1] != nil || results[0] != results[1] {
		t.Fatalf("two opens: tabs %p and %p, errors %v and %v", results[0], results[1], errs[0], errs[1])
	}
}

func TestHostAttachDuringExit(t *testing.T) {
	h := newHost()
	tab := &hostTab{id: "exiting"}
	h.tabs[tab.id] = tab
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			h.mu.Lock()
			tab.exited = i%2 == 0
			h.mu.Unlock()
		}
	}()
	for i := 0; i < 1000; i++ {
		if got, err := h.tab(hostRequest{Op: "attach", ID: tab.id}); err != nil || got != tab {
			t.Fatalf("attach during exit: %p, %v", got, err)
		}
	}
	<-done
}

func TestHostCloseWaitsForProcessExit(t *testing.T) {
	needTerminal(t)
	h := newHost()
	tab, err := h.tab(hostRequest{Op: "open", ID: "close", Command: []string{"sleep", "100"}, Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h.closeTab(tab.id)
	select {
	case <-tab.term.Done():
	default:
		t.Fatal("close returned before the process exited")
	}
}

func TestHostStartupWaitsForSocketOwner(t *testing.T) {
	needTerminal(t)
	data := t.TempDir()
	unlock, err := lockSocket(hostSocketPath(data))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- RunHost(data) }()
	select {
	case err := <-result:
		unlock()
		t.Fatalf("host startup finished while the lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := os.Stat(hostSocketPath(data)); !os.IsNotExist(err) {
		unlock()
		t.Fatalf("host bound its socket while the lock was held: %v", err)
	}
	unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := HostPID(data); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("host did not start after the lock was released")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := StopHost(data); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
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
	if b.tabs[0].shell() || !b.tabs[1].shell() || b.tabs[1].run != "echo running; sleep 100" || len(b.panes()) != 2 {
		mu.Unlock()
		b.Close()
		t.Fatalf("adopted as %+v, %+v", b.tabs[0], b.tabs[1])
	}
	// Quit and End Tabs asks before ending the live terminal, then the
	// host's programs end as the window does.
	var quit bool
	b.leave = func(q bool) { quit = q }
	var confirm func(bool)
	b.ask = func(_, _, _ string, then func(bool)) { confirm = then }
	if !slices.ContainsFunc(b.menuBar()[0].items, func(c command) bool { return c.label == "Quit and End Panes" }) {
		mu.Unlock()
		b.Close()
		t.Fatal("no Quit and End Panes")
	}
	b.quitEnding()
	ui.NewTester(b.confirmLeave, 600, 300).Frame()
	if !b.confirmingLeave || confirm == nil || quit {
		mu.Unlock()
		b.Close()
		t.Fatal("quit did not ask before ending the live terminal")
	}
	confirm(true)
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

// With the window closed, list, read, send, close and wait reach the
// tabs it left running in the host; the others say they need it.
func TestCommandsWithoutTheWindow(t *testing.T) {
	needTerminal(t)
	data, repo := fixture(t)
	testHost(t, data)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: data, Cwd: repo, Host: true})
	mu.Lock()
	a.setProjects(index(t, data))
	a.selectStart()
	shell := a.startShell(repo, "echo running; sleep 100")
	if len(a.tabs) != 2 || shell == nil {
		mu.Unlock()
		a.Close()
		t.Fatalf("%d tabs: %s", len(a.tabs), a.notice)
	}
	larik := a.tabs[0]
	mu.Unlock()
	a.Close()

	call := func(req Request) Response {
		t.Helper()
		res, err := Call(data, req)
		if err != nil {
			t.Fatalf("%s: %v", req.Cmd, err)
		}
		return res
	}
	res := call(Request{Cmd: "list"})
	if len(res.Tabs) != 2 || res.Tabs[0].ID != larik.id || res.Tabs[0].Kind != "larik" || res.Tabs[1].Kind != "terminal" || res.Tabs[1].Run != "echo running; sleep 100" {
		t.Fatalf("list: %+v", res.Tabs)
	}
	waitText := func(id, want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			res := call(Request{Cmd: "read", Tab: id})
			if strings.Contains(res.Text, want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("read %s: %q, want %q", id, res.Text, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	waitText(larik.id, "tab:"+larik.id)
	waitText(shell.id, "running")
	// Typed in, larik exits with 5, which wait reports as an error.
	call(Request{Cmd: "send", Tab: larik.id, Text: "5", Enter: true})
	res = call(Request{Cmd: "wait", Tab: larik.id, State: "error", Timeout: 5 * time.Second})
	if res.State != "error" || res.Tab == nil || res.Tab.Code != 5 {
		t.Fatalf("wait: %+v", res)
	}
	call(Request{Cmd: "close", Tab: shell.id})
	if res := call(Request{Cmd: "list"}); len(res.Tabs) != 1 {
		t.Fatalf("after close: %+v", res.Tabs)
	}
	if _, err := Call(data, Request{Cmd: "focus", Tab: larik.id}); err == nil || !strings.Contains(err.Error(), "window isn't open") {
		t.Fatalf("focus without the window: %v", err)
	}
	if _, err := Call(data, Request{Cmd: "read", Tab: "focused"}); err == nil {
		t.Fatal("a focused tab without the window")
	}
}

// host stop ends the host and what it runs, by asking it, or by its pid
// when it doesn't answer.
func TestStopHost(t *testing.T) {
	needTerminal(t)
	data := t.TempDir()
	ran := make(chan error, 1)
	go func() { ran <- RunHost(data) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := HostPID(data); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the host didn't start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid, _ := os.ReadFile(hostSocketPath(data) + ".pid"); string(pid) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("pid file: %q", pid)
	}
	conn, _, _, err := askHost(data, hostRequest{Op: "open", ID: "x", Command: []string{"sleep", "100"}, Dir: data})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := StopHost(data); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ran:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RunHost didn't return")
	}
	if _, err := os.Stat(hostSocketPath(data) + ".pid"); err == nil {
		t.Fatal("the pid file stayed")
	}
	if err := StopHost(data); err != ErrNoHost {
		t.Fatalf("stopping no host: %v", err)
	}
}
