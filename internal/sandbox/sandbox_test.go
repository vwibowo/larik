package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTest(t *testing.T, cfg Config) (*Sandbox, string, string) {
	t.Helper()
	root := real(t.TempDir())
	home := real(t.TempDir())
	os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755)
	sb, warn := New(cfg, root, home)
	if sb == nil {
		t.Skipf("no sandbox on this machine: %s", warn)
	}
	return sb, root, home
}

func run(t *testing.T, sb *Sandbox, dir, script string) (string, error) {
	t.Helper()
	out, err := sb.Command(script, dir).CombinedOutput()
	return string(out), err
}

func TestConfinement(t *testing.T) {
	sb, root, home := newTest(t, Config{})

	if out, err := run(t, sb, root, "echo ok > inside.txt && cat inside.txt"); err != nil || strings.TrimSpace(out) != "ok" {
		t.Fatalf("write inside project: %v %s", err, out)
	}
	if out, err := run(t, sb, root, `echo ok > "$TMPDIR/larik-sb-test" && cat "$TMPDIR/larik-sb-test"`); err != nil {
		t.Errorf("write to TMPDIR: %v %s", err, out)
	}
	// t.TempDir() lives under the per-user temp root, which the sandbox
	// allows; the real home directory does not.
	realHome, _ := os.UserHomeDir()
	outside := filepath.Join(realHome, fmt.Sprintf(".larik-sandbox-escape-%d", os.Getpid()))
	defer os.Remove(outside)
	if _, err := run(t, sb, root, "echo x > "+outside); err == nil {
		t.Error("write outside the project must fail")
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Error("file outside the project was created")
	}
	for _, p := range []string{".git/hooks/pre-commit", ".git/config", ".larik/settings.json", ".mcp.json"} {
		if out, err := run(t, sb, root, "mkdir -p $(dirname "+p+") 2>/dev/null; echo x > "+p); err == nil {
			t.Errorf("write to protected %s must fail: %s", p, out)
		}
	}
	// Reading outside is fine.
	os.WriteFile(filepath.Join(home, "readable.txt"), []byte("r"), 0o644)
	if out, err := run(t, sb, root, "cat "+filepath.Join(home, "readable.txt")); err != nil || out != "r" {
		t.Errorf("read outside: %v %q", err, out)
	}
}

func TestNetwork(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 needed")
	}
	sb, root, _ := newTest(t, Config{})
	// Localhost works (tests that start local servers keep passing)...
	local := `python3 -c "
import http.server,threading,urllib.request
s=http.server.HTTPServer(('127.0.0.1',0),http.server.SimpleHTTPRequestHandler)
threading.Thread(target=s.serve_forever,daemon=True).start()
print(urllib.request.urlopen('http://127.0.0.1:%d/'%s.server_port).status)"`
	if out, err := run(t, sb, root, local); err != nil || !strings.Contains(out, "200") {
		t.Errorf("localhost should work: %v %s", err, out)
	}
	// ...but outbound connections don't (raw IP, no DNS involved).
	remote := `python3 -c "
import socket; s=socket.socket(); s.settimeout(3); s.connect(('1.1.1.1', 80)); print('CONN' + 'ECTED')"`
	// The marker is split in the source so a traceback echoing the line
	// can't match it.
	if out, _ := run(t, sb, root, remote); strings.Contains(out, "CONNECTED") {
		t.Error("outbound network must be blocked")
	}
}

func TestNoAppleEventEscape(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS only")
	}
	sb, root, _ := newTest(t, Config{})
	out, _ := run(t, sb, root, `osascript -e 'tell application "Finder" to get count of windows'`)
	if !strings.Contains(out, "error") {
		t.Errorf("Apple Events should be blocked, got %q", out)
	}
}

func TestDisabled(t *testing.T) {
	off := false
	if sb, _ := New(Config{Enabled: &off}, t.TempDir(), t.TempDir()); sb != nil {
		t.Error("disabled config must return nil")
	}
}

func TestProfileAndArgs(t *testing.T) {
	s := &Sandbox{root: "/w/p", writable: []string{"/w/p", `/odd "path"`}, protected: []string{"/w/p/.git/hooks"}}
	prof := s.seatbeltProfile()
	allow := strings.Index(prof, `(subpath "/w/p")`)
	deny := strings.Index(prof, `(deny file-write*`)
	if allow < 0 || deny < allow || !strings.Contains(prof, `(subpath "/odd \"path\"")`) {
		t.Errorf("profile:\n%s", prof)
	}
	if strings.Contains(prof, "(allow network*)") {
		t.Error("network must be off by default")
	}
	s.network = true
	if !strings.Contains(s.seatbeltProfile(), "(allow network*)") {
		t.Error("network config not applied")
	}

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755)
	b := &Sandbox{kind: "bubblewrap", root: dir, writable: []string{dir, "/definitely/missing"}, protected: []string{filepath.Join(dir, ".git", "hooks"), filepath.Join(dir, ".larik")}}
	args := strings.Join(b.bwrapArgs("make test", dir), " ")
	for _, want := range []string{"--ro-bind / /", "--bind " + dir + " " + dir, "--ro-bind " + dir + "/.git/hooks", "--unshare-net", "--chdir " + dir + " -- bash -c make test"} {
		if !strings.Contains(args, want) {
			t.Errorf("bwrap args missing %q:\n%s", want, args)
		}
	}
	if strings.Contains(args, "/definitely/missing") || strings.Contains(args, ".larik") {
		t.Errorf("missing paths must be skipped:\n%s", args)
	}
	// Protected re-binds must come after the writable binds to take effect.
	if strings.Index(args, "--ro-bind "+dir+"/.git/hooks") < strings.Index(args, "--bind "+dir) {
		t.Error("protected binds must follow writable binds")
	}
}
