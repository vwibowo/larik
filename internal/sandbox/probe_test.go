package sandbox

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestProbeHint(t *testing.T) {
	for _, c := range []struct {
		kind, msg string
		container bool
		want      string
	}{
		{"bubblewrap", "bwrap: Can't mount proc on /proc: Operation not permitted", false, "systempaths=unconfined"},
		{"bubblewrap", "bwrap: setting up uid map: Permission denied", false, "user namespaces"},
		{"bubblewrap", "bwrap: No permissions to create new namespace", false, "user namespaces"},
		// In a container, Docker's defaults fail on namespaces first; the
		// fix is the container's options either way.
		{"bubblewrap", "bwrap: No permissions to create new namespace", true, "seccomp=unconfined --security-opt systempaths=unconfined"},
		{"bubblewrap", "bwrap: Can't mount proc on /proc: Operation not permitted", true, "Running Larik in a container"},
		{"seatbelt", "sandbox-exec: sandbox_apply: Operation not permitted", false, "inside another sandbox"},
		{"bubblewrap", "something else", false, ""},
	} {
		got := probeHint(c.kind, c.msg, c.container)
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("probeHint(%s, %q) = %q, want it to mention %q", c.kind, c.msg, got, c.want)
		}
	}
}

// brokenSandboxEnv marks a run of this test binary where the sandbox is
// expected not to start: under another sandbox (macOS, set by the test
// below), or in a container with Docker's default options (Linux, set by
// hand).
const brokenSandboxEnv = "LARIK_EXPECT_BROKEN_SANDBOX"

// nestingProfile lets ordinary programs run but, being deny-by-default,
// makes a nested sandbox-exec fail with sandbox_apply.
const nestingProfile = `(version 1)(deny default)(allow process*)(allow file*)(allow sysctl*)(allow mach*)` +
	`(allow ipc*)(allow signal)(allow network*)(allow system-socket)(allow iokit*)(allow user-preference-read)(allow pseudo-tty)`

func TestNewFallsBackWhenTheSandboxCantStart(t *testing.T) {
	if os.Getenv(brokenSandboxEnv) == "1" {
		dir := t.TempDir()
		sb, warn := New(Config{}, dir, dir)
		if sb != nil {
			sb.Close()
			t.Fatal("a sandbox that can't start must not be used")
		}
		want := "inside another sandbox"
		if runtime.GOOS == "linux" {
			want = "seccomp=unconfined --security-opt systempaths=unconfined"
		}
		if !strings.Contains(warn, "can't start here") || !strings.Contains(warn, "ask for approval") || !strings.Contains(warn, want) {
			t.Fatalf("the warning should say why and what to do: %s", warn)
		}
		return
	}
	if runtime.GOOS != "darwin" {
		t.Skip("on Linux, run in a container with Docker's default options and " + brokenSandboxEnv + "=1")
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		t.Skip("sandbox-exec not found")
	}
	cmd := exec.Command("sandbox-exec", "-p", nestingProfile, os.Args[0], "-test.run", "^TestNewFallsBackWhenTheSandboxCantStart$", "-test.v")
	cmd.Env = append(os.Environ(), brokenSandboxEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS") {
		t.Fatalf("under another sandbox: %v\n%s", err, out)
	}
}
