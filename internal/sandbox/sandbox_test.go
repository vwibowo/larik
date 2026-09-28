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
	t.Cleanup(func() { sb.Close() })
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

func TestWorktreeSandbox(t *testing.T) {
	// The repository must live outside the temp dirs the sandbox always
	// allows, as real projects do.
	realHome, _ := os.UserHomeDir()
	root, err := os.MkdirTemp(realHome, ".larik-sandbox-wt-")
	if err != nil {
		t.Skip(err)
	}
	defer os.RemoveAll(root)
	root = real(root)
	sb, _ := New(Config{}, root, real(t.TempDir()))
	if sb == nil {
		t.Skip("no sandbox on this machine")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	git := func(dir string, args ...string) {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git(root, "init", "-q")
	git(root, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(real(t.TempDir()), "wt")
	git(root, "worktree", "add", "-q", "-b", "larik/t", wt)
	w := sb.ForWorktree(wt, filepath.Join(root, ".git"))

	// The worktree is writable, and committing there works: git writes
	// objects and refs into the main repository's .git.
	if out, err := run(t, w, wt, `echo hi > f.txt && git -c user.name=t -c user.email=t@t add f.txt && git -c user.name=t -c user.email=t@t commit -qm wt && echo ok`); err != nil || !strings.Contains(out, "ok") {
		t.Fatalf("commit in worktree: %v %s", err, out)
	}
	// The original checkout is not writable, nor are the shared hooks and
	// config or the worktree's .git pointer.
	for _, p := range []string{filepath.Join(root, "escape.txt"), filepath.Join(root, ".git", "hooks", "pre-commit"), filepath.Join(root, ".git", "config"), filepath.Join(wt, ".git")} {
		if _, err := run(t, w, wt, "echo x >> "+p); err == nil {
			t.Errorf("write to %s must fail", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); err == nil {
		t.Error("escaped into the original checkout")
	}
}

// TestSystemTempDirIsNotWritable is the regression test for the incident
// that prompted this: a sandboxed command must not be able to touch the
// literal, machine-wide /tmp -- the shared directory a stray "rm -rf"
// once reached, deleting another program's files -- even though its own
// private scratch directory, and (on macOS) the per-user temp root that
// real tools like mktemp resolve to regardless of $TMPDIR, still work.
func TestSystemTempDirIsNotWritable(t *testing.T) {
	sb, root, _ := newTest(t, Config{})

	target := filepath.Join("/tmp", fmt.Sprintf("larik-sandbox-escape-%d.txt", os.Getpid()))
	defer os.Remove(target)
	if _, err := run(t, sb, root, "echo x > "+target); err == nil {
		t.Errorf("write to %s must fail", target)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Error("escaped into /tmp")
	}
	if runtime.GOOS != "darwin" {
		// Elsewhere os.TempDir() is ordinarily just another name for
		// /tmp, so it must be denied the same way.
		other := filepath.Join(real(os.TempDir()), fmt.Sprintf("larik-sandbox-escape2-%d.txt", os.Getpid()))
		defer os.Remove(other)
		if _, err := run(t, sb, root, "echo x > "+other); err == nil {
			t.Errorf("write to %s must fail", other)
		}
	}

	// The sandbox's own private temp directory, which $TMPDIR points to
	// inside the sandbox, works for tools that honor it...
	if out, err := run(t, sb, root, `t=$(mktemp "$TMPDIR/tmp.XXXXXX") && echo ok > "$t" && cat "$t"`); err != nil || strings.TrimSpace(out) != "ok" {
		t.Errorf("an explicit $TMPDIR template should work: %v %s", err, out)
	}
	// ...and bare mktemp, which on macOS resolves to the per-user temp
	// root via confstr rather than $TMPDIR, still works too.
	if out, err := run(t, sb, root, `t=$(mktemp) && echo ok > "$t" && cat "$t"`); err != nil || strings.TrimSpace(out) != "ok" {
		t.Errorf("bare mktemp inside the sandbox should work: %v %s", err, out)
	}
}

// TestClosePerSandboxTempDir checks that each Sandbox gets its own
// private directory, and that Close removes it without disturbing an
// unrelated Sandbox's.
func TestClosePerSandboxTempDir(t *testing.T) {
	a, root, home := newTest(t, Config{})
	b, _ := New(Config{}, root, home)
	if b == nil {
		t.Skip("no sandbox on this machine")
	}
	defer b.Close()

	if a.tmpDir == "" || a.tmpDir == b.tmpDir {
		t.Fatalf("each sandbox should get its own private directory, got %q and %q", a.tmpDir, b.tmpDir)
	}
	if _, err := os.Stat(a.tmpDir); err != nil {
		t.Fatalf("tmpDir should exist: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if _, err := os.Stat(a.tmpDir); !os.IsNotExist(err) {
		t.Error("Close should remove the private directory")
	}
	if _, err := os.Stat(b.tmpDir); err != nil {
		t.Error("closing one sandbox must not remove another's directory")
	}
}

func TestGitDirCannotBeRedirected(t *testing.T) {
	sb, root, _ := newTest(t, Config{})
	for _, script := range []string{
		"echo /tmp/x > .git/commondir",
		"echo x > .git/config.worktree",
		"mkdir -p .git/modules/m && echo x > .git/modules/m/config",
		"mv .git .git-moved",
		"rm -rf .git",
	} {
		if out, err := run(t, sb, root, script); err == nil {
			t.Errorf("%q must fail: %s", script, out)
		}
	}
	if fi, err := os.Stat(filepath.Join(root, ".git")); err != nil || !fi.IsDir() {
		t.Fatalf(".git was moved or removed: %v", err)
	}
	// Ordinary repository writes still work.
	if out, err := run(t, sb, root, "mkdir -p .git/objects/ab && echo x > .git/objects/ab/cd && echo ref > .git/HEAD.test"); err != nil {
		t.Errorf("writing objects must work: %v %s", err, out)
	}
}

func TestSandboxedCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git needed")
	}
	root := real(t.TempDir())
	if out, err := exec.Command("git", "init", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	sb, warn := New(Config{}, root, real(t.TempDir()))
	if sb == nil {
		t.Skipf("no sandbox on this machine: %s", warn)
	}
	t.Cleanup(func() { sb.Close() })
	script := "echo hi > a.txt && git add a.txt && git -c user.name=t -c user.email=t@t commit -qm first && git log --oneline | wc -l"
	if out, err := run(t, sb, root, script); err != nil || strings.TrimSpace(out) != "1" {
		t.Fatalf("commit inside the sandbox: %v %s", err, out)
	}
}
