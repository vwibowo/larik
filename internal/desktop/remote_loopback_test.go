package desktop

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/gorilla/websocket"
)

// These tests use a disposable SSH server on this machine. They exercise the
// real SSH transport without requiring a second machine or an API key.
func loopbackSSH(t *testing.T) (string, dialer) {
	t.Helper()
	if os.Getenv("LARIK_DESKTOP_SSH_LOOPBACK") != "1" || os.Getenv("LARIK_DESKTOP_SSH") == "" {
		t.Skip("set LARIK_DESKTOP_SSH_LOOPBACK=1 and LARIK_DESKTOP_SSH for the loopback SSH tests")
	}
	name := os.Getenv("LARIK_DESKTOP_SSH")
	dial, _, stop, _, err := sshConnect(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	features, _, err := probeHost(dial, name)
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range []string{"remote-files", "remote-git", "remote-worktrees", "remote-meta"} {
		if !slicesContains(features, feature) {
			t.Fatalf("host is missing %s: %v", feature, features)
		}
	}
	return name, dial
}

func TestSSHLoopbackProjectOperations(t *testing.T) {
	name, dial := loopbackSSH(t)
	repo := gitRepo(t)
	gitIn(t, repo, "config", "user.name", "Larik test")
	gitIn(t, repo, "config", "user.email", "larik-test@localhost")
	files := remoteFiles{name: name, root: repo, dial: dial}
	fileCall := func(op, path, filename string) remoteFileReply {
		t.Helper()
		reply, err := files.call(remoteFileRequest{Op: op, Path: remotePath(name, path), Name: filename})
		if err != nil {
			t.Fatalf("file %s: %v", op, err)
		}
		return reply
	}
	if got := fileCall("preview", filepath.Join(repo, "a.txt"), "").Text; got != "one\n" {
		t.Fatalf("remote preview: %q", got)
	}
	created := fileCall("create-file", repo, "notes.txt").Dest
	if created != filepath.Join(repo, "notes.txt") {
		t.Fatalf("remote create: %q", created)
	}
	if got := fileCall("rename", created, "renamed.txt").Dest; got != filepath.Join(repo, "renamed.txt") {
		t.Fatalf("remote rename: %q", got)
	}
	fileCall("trash", filepath.Join(repo, "renamed.txt"), "")
	if _, err := os.Stat(filepath.Join(repo, "renamed.txt")); !os.IsNotExist(err) {
		t.Fatalf("remote trash left file: %v", err)
	}
	if got := fileCall("list", repo, "").Entries; len(got) != 1 || got[0].Path != filepath.Join(repo, "a.txt") {
		t.Fatalf("remote list: %+v", got)
	}

	project := remoteProject{name: name, dial: dial}
	projectCall := func(req remoteProjectRequest) remoteProjectReply {
		t.Helper()
		req.Dir = remotePath(name, repo)
		reply, err := project.call(req)
		if err != nil {
			t.Fatalf("project %s: %v", req.Op, err)
		}
		return reply
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectCall(remoteProjectRequest{Op: "read"}).Changes; got == nil || len(got.Files) != 1 || got.Files[0].New != "one\ntwo\n" {
		t.Fatalf("remote changes: %+v", got)
	}
	projectCall(remoteProjectRequest{Op: "stage", Path: "a.txt"})
	if got := gitIn(t, repo, "diff", "--cached", "--name-only"); got != "a.txt" {
		t.Fatalf("remote stage: %q", got)
	}
	projectCall(remoteProjectRequest{Op: "commit", Message: "SSH test"})
	if got := gitIn(t, repo, "log", "-1", "--format=%s"); got != "SSH test" {
		t.Fatalf("remote commit: %q", got)
	}
	w := projectCall(remoteProjectRequest{Op: "worktree-create", Name: "ssh-test"}).Worktree
	if w == nil || !strings.HasPrefix(w.Path, remotePath(name, "")) {
		t.Fatalf("remote worktree: %+v", w)
	}
	_, workdir, _ := splitRemote(w.Path)
	if err := os.WriteFile(filepath.Join(workdir, "from-worktree.txt"), []byte("merged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := projectCall(remoteProjectRequest{Op: "worktree-merge", Worktree: w.Path, Message: "SSH merge"}).Merged; got == nil || !got.Committed {
		t.Fatalf("remote worktree merge: %+v", got)
	}
	projectCall(remoteProjectRequest{Op: "worktree-remove", Worktree: w.Path})
	if _, err := os.Stat(filepath.Join(repo, "from-worktree.txt")); err != nil {
		t.Fatalf("merged remote file: %v", err)
	}
}

func TestSSHLoopbackInterruptedForwardKeepsTab(t *testing.T) {
	if os.Getenv("LARIK_DESKTOP_SSH_LOOPBACK") != "1" || os.Getenv("LARIK_DESKTOP_SSH") == "" {
		t.Skip("loopback SSH is not configured")
	}
	needTerminal(t)
	name := os.Getenv("LARIK_DESKTOP_SSH")
	a, mu := serialApp(Options{DataDir: t.TempDir()})
	defer a.Close()
	tt := ui.NewTester(a.view, 1240, 800)
	mu.Lock()
	a.connectServer(name)
	mu.Unlock()
	frameUntil(t, mu, tt, "SSH connection", func() bool { return a.dialOf(a.servers[0]) != nil })
	mu.Lock()
	tb := a.startShell(remotePath(name, ""), "echo larik-live-start; sleep 2; echo larik-live-continued; sleep 30")
	if tb == nil {
		mu.Unlock()
		t.Fatal("remote terminal did not start")
	}
	a.activate(tb)
	oldTerm, oldPane := tb.term, a.paneOf(tb)
	stop := a.serverNamed(name).stop
	mu.Unlock()
	frameUntil(t, mu, tt, "remote process start", func() bool { return strings.Contains(tb.term.Text(), "larik-live-start") })
	stop() // The host and its process stay alive; only the SSH forward drops.
	frameUntil(t, mu, tt, "disconnected tab", func() bool { return tb.disconnected && a.dialOf(a.servers[0]) == nil })
	frameUntil(t, mu, tt, "automatic reconnection", func() bool { return !tb.disconnected && a.dialOf(a.servers[0]) != nil })
	frameUntil(t, mu, tt, "continued remote output", func() bool { return strings.Contains(tb.term.Text(), "larik-live-continued") })
	mu.Lock()
	if a.tabByID(tb.id) != tb || tb.term == oldTerm || a.paneOf(tb) != oldPane || len(a.tabs) != 1 {
		mu.Unlock()
		t.Fatal("reconnection duplicated or moved the remote tab")
	}
	dial := a.dialOf(a.servers[0])
	mu.Unlock()
	if conn, _, _, err := askHostVia(dial, hostRequest{Op: "close", ID: tb.id}); err == nil {
		conn.Close()
	}
	mu.Lock()
	a.disconnectServer(name)
	mu.Unlock()
}

func TestSSHLoopbackHTTPAndWebSocketForward(t *testing.T) {
	name, _ := loopbackSSH(t)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws" {
			_, _ = w.Write([]byte("remote-http-ok"))
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		kind, data, err := conn.ReadMessage()
		if err == nil {
			_ = conn.WriteMessage(kind, data)
		}
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	port, _ := strconv.Atoi(u.Port())
	local, stop, health, err := forwardRemotePort(name, port)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	address := "127.0.0.1:" + strconv.Itoa(local)
	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(response.Body, 64))
	if err != nil {
		t.Fatal(err)
	}
	if string(buf) != "remote-http-ok" {
		t.Fatalf("forwarded HTTP: %q", buf)
	}
	conn, _, err := websocket.DefaultDialer.Dial("ws://"+address+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteMessage(websocket.TextMessage, []byte("through-ssh")); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.ReadMessage()
	if err != nil || string(data) != "through-ssh" {
		t.Fatalf("forwarded WebSocket: %q, %v", data, err)
	}
	stop()
	select {
	case <-health:
	case <-time.After(3 * time.Second):
		t.Fatal("SSH forward did not stop")
	}
}
