package sandbox

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// confined runs argv under Confine, with an absolute argv[0].
func confined(t *testing.T, sb *Sandbox, argv ...string) (string, error) {
	t.Helper()
	exe, err := exec.LookPath(argv[0])
	if err != nil {
		t.Skipf("%s not found", argv[0])
	}
	argv[0] = real(exe)
	wrapped := sb.Confine(argv)
	out, err := exec.Command(wrapped[0], wrapped[1:]...).CombinedOutput()
	return string(out), err
}

// mustRun checks that confined programs run at all, so a test that
// expects something to be blocked can't pass because nothing ran.
func mustRun(t *testing.T, sb *Sandbox) {
	t.Helper()
	if out, err := confined(t, sb, "echo", "hello"); err != nil || strings.TrimSpace(out) != "hello" {
		t.Fatalf("a confined program should run: %v %q", err, out)
	}
}

func TestConfineRunsTheProgram(t *testing.T) {
	sb, _, _ := newTest(t, Config{})
	mustRun(t, sb)
}

func TestConfineHidesPersonalFiles(t *testing.T) {
	sb, root, home := newTest(t, Config{})
	mustRun(t, sb)
	for _, dir := range []string{home, root} { // a home and a temp directory
		secret := filepath.Join(dir, "secret.txt")
		os.WriteFile(secret, []byte("s3cret"), 0o600)
		if out, err := exec.Command("cat", secret).CombinedOutput(); err != nil || string(out) != "s3cret" {
			t.Fatalf("control: cat outside the sandbox should work: %v %q", err, out)
		}
		if out, _ := confined(t, sb, "cat", secret); strings.Contains(out, "s3cret") {
			t.Errorf("a confined program read %s", secret)
		}
	}
	// System files stay readable.
	if out, err := confined(t, sb, "cat", "/etc/hosts"); err != nil {
		t.Errorf("system files should be readable: %v %s", err, out)
	}
}

func TestConfineBlocksWrites(t *testing.T) {
	sb, root, _ := newTest(t, Config{})
	mustRun(t, sb)
	// Even the project and the sandbox's own temp directory, which bash
	// commands may write to.
	for _, p := range []string{filepath.Join(root, "made.txt"), filepath.Join(sb.tmpDir, "made.txt")} {
		confined(t, sb, "touch", p)
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("a confined program created %s", p)
		}
	}
}

func TestConfineBlocksLocalhost(t *testing.T) {
	if _, err := exec.LookPath("nc"); err != nil {
		t.Skip("nc needed")
	}
	sb, _, _ := newTest(t, Config{Network: true}) // even with bash's network on
	mustRun(t, sb)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
	if out, err := exec.Command("nc", "-z", "-w", "2", "127.0.0.1", port).CombinedOutput(); err != nil {
		t.Fatalf("control: nc outside the sandbox should connect: %v %s", err, out)
	}
	if _, err := confined(t, sb, "nc", "-z", "-w", "2", "127.0.0.1", port); err == nil {
		t.Error("a confined program reached localhost")
	}
}

func TestConfineBlocksOtherPrograms(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("on Linux a started program is confined the same way, not blocked")
	}
	sb, _, _ := newTest(t, Config{})
	mustRun(t, sb)
	if out, err := confined(t, sb, "sh", "-c", "/bin/echo escaped"); err == nil || strings.Contains(out, "escaped") {
		t.Errorf("a confined program started another: %v %q", err, out)
	}
}

func TestConfineArgs(t *testing.T) {
	bw := (&Sandbox{kind: "bubblewrap", home: "/srv/me"}).Confine([]string{"/opt/larik/larik", "--x"})
	line := strings.Join(bw, " ")
	for _, want := range []string{"bwrap --die-with-parent --new-session --unshare-all --cap-drop ALL --ro-bind / /", "--ro-bind /opt/larik/larik /opt/larik/larik", "-- /opt/larik/larik --x"} {
		if !strings.Contains(line, want) {
			t.Errorf("bubblewrap args lack %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, "--share-net") || strings.Contains(line, "--bind ") {
		t.Errorf("nothing may be writable or networked:\n%s", line)
	}
	if !slicesContain(hiddenDirs("/srv/me"), "/srv/me") || slicesContain(hiddenDirs("/home/me"), "/home/me") {
		t.Error("a home outside /home is hidden on its own; one inside is covered by /home")
	}

	p := confineProfile("/opt/larik/larik", "/Users/me")
	for _, want := range []string{"(deny default)", `(allow process-exec (literal "/opt/larik/larik"))`, `(subpath "/Users/me")`, `(allow file-read* (literal "/opt/larik/larik"))`} {
		if !strings.Contains(p, want) {
			t.Errorf("profile lacks %q:\n%s", want, p)
		}
	}
	for _, banned := range []string{"file-write", "network", "mach-lookup"} {
		if strings.Contains(p, banned) {
			t.Errorf("profile must not allow %s:\n%s", banned, p)
		}
	}
}

func slicesContain(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
