package desktop

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
)

func TestRemotePaths(t *testing.T) {
	p := remotePath("devbox", "/home/me/app")
	if p != "ssh://devbox/home/me/app" || !isRemote(p) || isRemote("/home/me/app") {
		t.Fatalf("marked %q", p)
	}
	for in, want := range map[string][3]string{
		"ssh://devbox/home/me": {"devbox", "/home/me", "true"},
		"ssh://me@box":         {"me@box", "", "true"},
		"/Users/me":            {"", "/Users/me", "false"},
	} {
		name, path, ok := splitRemote(in)
		if name != want[0] || path != want[1] || (ok != (want[2] == "true")) {
			t.Errorf("splitRemote(%q) = %q %q %v", in, name, path, ok)
		}
	}
	socket, bin, err := parseEnsure("/tmp/larik-desktop-host-abc.sock\n/home/me/go/bin/larik\n")
	if err != nil || socket != "/tmp/larik-desktop-host-abc.sock" || bin != "/home/me/go/bin/larik" {
		t.Fatalf("parseEnsure: %q %q %v", socket, bin, err)
	}
	if _, _, err := parseEnsure("zsh: command not found: larik\n"); err == nil {
		t.Fatal("parseEnsure took an error for a socket")
	}
	for out, want := range map[string]string{
		"zsh: command not found: larik":       "isn't on the PATH",
		"bash: line 1: larik: not found":      "isn't on the PATH",
		"larik: bubbletea: error opening TTY": "0.28.0 or newer",
	} {
		if err := ensureError("devbox", out, errors.New("exit status 1")); !strings.Contains(err.Error(), want) {
			t.Errorf("ensureError(%q) = %v, want %q", out, err, want)
		}
	}
	if got := strings.Join(sshForwardArgs("devbox", "/tmp/l.sock", "/tmp/r.sock"), " "); !strings.Contains(got, "-N") || !strings.HasSuffix(got, "-L /tmp/l.sock:/tmp/r.sock devbox") || !strings.Contains(got, "BatchMode=yes") {
		t.Fatalf("forward args %q", got)
	}
	if got := strings.Join(sshEnsureArgs("devbox"), " "); !strings.HasSuffix(got, "devbox larik desktop host ensure") {
		t.Fatalf("ensure args %q", got)
	}
	t.Setenv("LARIK_DESKTOP_REMOTE", "~/bin/larik")
	if got := sshEnsureArgs("devbox"); got[len(got)-1] != "~/bin/larik desktop host ensure" {
		t.Fatalf("ensure args with LARIK_DESKTOP_REMOTE: %q", got)
	}
	hosts := parseSSHHosts("Host devbox build-*\n  HostName 10.0.0.2\nhost mini\nHost *\nMatch all\nHost devbox\n")
	if !slices.Equal(hosts, []string{"devbox", "mini"}) {
		t.Fatalf("hosts %q", hosts)
	}
	if got := remoteShellCommand("make dev"); got[0] != "/bin/sh" || got[len(got)-1] != "make dev" {
		t.Fatalf("shell %q", got)
	}
}

// standIn runs a host on this machine for data, as a server's would be,
// and returns how a window reaches it, as SSH would.
func standIn(t *testing.T, data string) (*host, connector) {
	t.Helper()
	b := make([]byte, 4)
	rand.Read(b)
	sock := filepath.Join(os.TempDir(), "larik-test-"+hex.EncodeToString(b)+".sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	h := newHost()
	h.dataDir, h.scratch = data, true
	go h.serve(ln)
	t.Cleanup(func() { ln.Close(); h.closeAll(); os.Remove(sock) })
	bin := fakeLarik(t)
	return h, func(name string) (dialer, string, func(), error) {
		return func() (net.Conn, error) { return net.Dial("unix", sock) }, bin, func() {}, nil
	}
}

// A server's sessions show under its name, and one opened runs in its
// host, in its folder there, with larik there; disconnecting leaves the
// program running, and connecting again shows its tab again.
func TestRemoteServer(t *testing.T) {
	needTerminal(t)
	local, _ := fixture(t)
	remote, repo := fixture(t)
	h, connect := standIn(t, remote)
	a, mu := serialApp(Options{Bin: fakeLarik(t), DataDir: local})
	defer a.Close()
	a.connect = connect

	mu.Lock()
	a.setProjects(index(t, local))
	a.connectServer("devbox")
	mu.Unlock()
	tt := ui.NewTester(a.view, 1240, 800)
	frameUntil(t, mu, tt, "connected", func() bool { return a.dialOf(a.servers[0]) != nil })
	got, failed := a.remoteIndexes()
	mu.Lock()
	a.setIndex(index(t, local), got, failed)
	tt.Frame()
	if len(failed) != 0 || !tt.HasText("devbox") || !tt.HasText(thisMachine()) || len(a.servers[0].projects) != 2 {
		mu.Unlock()
		t.Fatalf("failed %v, texts %q", failed, tt.Texts())
	}
	var parser string
	for _, p := range a.servers[0].projects {
		for _, s := range p.Sessions {
			if s.Title == "Refactor the parser and run the tests" {
				parser = s.Path
			}
		}
	}
	if !strings.HasPrefix(parser, "ssh://devbox/") {
		mu.Unlock()
		t.Fatalf("the server's session is at %q", parser)
	}
	snapshot(t, tt, "remote-sidebar.png")
	a.selectSession(parser)
	tb := a.tabOf(parser)
	if tb == nil || !tb.hosted || a.notice != "" {
		mu.Unlock()
		t.Fatalf("tab %v, notice %q", tb, a.notice)
	}
	list := h.list()
	if len(list) != 1 || list[0].Dir != repo || !strings.HasSuffix(list[0].Command[0], "/larik") || list[0].Command[1] != "--resume" {
		mu.Unlock()
		t.Fatalf("the host runs %+v", list)
	}
	mu.Unlock()
	frameUntil(t, mu, tt, "larik there", func() bool {
		text := tb.term.Text()
		return strings.Contains(text, "args:--resume") && strings.Contains(text, filepath.Base(repo))
	})

	// Saved without its tabs, which its host keeps, but with the server.
	mu.Lock()
	st := a.state()
	if !slices.Equal(st.Servers, []string{"devbox"}) || slices.Contains(st.Tabs, parser) {
		mu.Unlock()
		t.Fatalf("saved %+v", st)
	}
	a.disconnectServer("devbox")
	if len(a.tabs) != 0 || len(a.servers) != 0 || slices.ContainsFunc(a.projects, func(p session.Project) bool { return isRemote(p.Root) }) {
		mu.Unlock()
		t.Fatalf("after disconnecting: %d tabs, %d servers", len(a.tabs), len(a.servers))
	}
	if l := h.list(); len(l) != 1 || l[0].Exited {
		mu.Unlock()
		t.Fatalf("disconnecting ended the program: %+v", l)
	}
	a.connectServer("devbox")
	mu.Unlock()
	frameUntil(t, mu, tt, "its tab again", func() bool { return len(a.tabs) == 1 && a.tabs[0].id == list[0].ID })
	mu.Lock()
	defer mu.Unlock()
	if a.tabs[0].dir != remotePath("devbox", repo) {
		t.Fatalf("adopted in %q", a.tabs[0].dir)
	}
	// Not connected: a server's folder doesn't start a tab here.
	a.disconnectServer("devbox")
	if a.startTab(remotePath("devbox", repo), "", "") != nil || !strings.Contains(a.notice, "not connected to devbox") {
		t.Fatalf("started without a connection: %q", a.notice)
	}
}

func TestRemoteNeedsNewerHost(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(os.TempDir(), "larik-test-old-"+filepath.Base(dir)+".sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(sock)
	defer ln.Close()
	go func() { // an older host: ping says no protocol
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 512)
			conn.Read(buf)
			conn.Write([]byte(`{"pid":1}` + "\n"))
			conn.Close()
		}
	}()
	err = checkProto(func() (net.Conn, error) { return net.Dial("unix", sock) }, "devbox")
	if err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("an old host: %v", err)
	}
}

// Over real SSH, to the server LARIK_DESKTOP_SSH names, with the larik
// there LARIK_DESKTOP_REMOTE says, or larik: connecting, the index, a
// terminal tab there, and the tab again after disconnecting.
func TestSSHLive(t *testing.T) {
	name := os.Getenv("LARIK_DESKTOP_SSH")
	if name == "" {
		t.Skip("LARIK_DESKTOP_SSH names no server")
	}
	needTerminal(t)
	a, mu := serialApp(Options{DataDir: t.TempDir()})
	defer a.Close()
	tt := ui.NewTester(a.view, 1240, 800)
	mu.Lock()
	a.connectServer(name)
	mu.Unlock()
	frameUntil(t, mu, tt, "connected over SSH", func() bool {
		if a.servers[0].err != "" {
			t.Fatalf("connecting: %s", a.servers[0].err)
		}
		return a.dialOf(a.servers[0]) != nil
	})
	if _, failed := a.remoteIndexes(); len(failed) != 0 {
		t.Fatalf("index: %v", failed)
	}
	mu.Lock()
	tb := a.startShell(remotePath(name, ""), "echo larik-ssh-ok; uname -s; pwd; sleep 60")
	if tb == nil {
		mu.Unlock()
		t.Fatalf("no tab there: %q", a.notice)
	}
	a.activate(tb)
	id := tb.id
	mu.Unlock()
	frameUntil(t, mu, tt, "the shell there", func() bool { return strings.Contains(tb.term.Text(), "larik-ssh-ok") })
	mu.Lock()
	t.Logf("the tab there shows %q", strings.TrimSpace(tb.term.Text()))
	a.disconnectServer(name)
	a.connectServer(name)
	mu.Unlock()
	frameUntil(t, mu, tt, "the tab again", func() bool { return a.tabByID(id) != nil })
	mu.Lock()
	defer mu.Unlock()
	if conn, _, _, err := askHostVia(a.dialOf(a.servers[0]), hostRequest{Op: "close", ID: id}); err == nil {
		conn.Close()
	}
	a.disconnectServer(name)
}
