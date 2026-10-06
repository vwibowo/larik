package hooks

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The "tests must pass before the turn ends" recipe in the README is a real
// script. This runs it the way Larik does, in a throwaway git repository.
func TestRequireTestsRecipe(t *testing.T) {
	for _, tool := range []string{"sh", "git", "cksum", "xargs"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
	script, err := filepath.Abs("../../docs/examples/require-tests.sh")
	if err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	tmp := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "marker"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-qm", "init")

	// run is the hook as Larik starts it: the payload on stdin, the project
	// directory in the environment. verify is the project's test command.
	run := func(session, verify string) (int, string) {
		t.Helper()
		cmd := exec.Command(script)
		cmd.Stdin = strings.NewReader(`{"session_id":"` + session + `","hook_event_name":"Stop"}`)
		cmd.Env = append(os.Environ(), "LARIK_PROJECT_DIR="+repo, "TMPDIR="+tmp, "VERIFY_CMD="+verify)
		out, err := cmd.CombinedOutput()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), string(out)
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0, string(out)
	}

	if code, out := run("s1", "echo 'FAIL: TestAdd'; exit 1"); code != 2 || !strings.Contains(out, "Tests failed") || !strings.Contains(out, "FAIL: TestAdd") {
		t.Fatalf("failing tests must block with their output: exit %d\n%s", code, out)
	}
	if code, out := run("s1", "exit 0"); code != 0 || out != "" {
		t.Fatalf("passing tests let the turn end quietly: exit %d\n%s", code, out)
	}
	// Green for exactly these changes: the tests are not run again.
	if code, out := run("s1", "echo RAN; exit 1"); code != 0 || out != "" {
		t.Fatalf("unchanged since the last green run should skip the tests: exit %d\n%s", code, out)
	}
	// A different session has its own record.
	if code, _ := run("s2", "exit 1"); code != 2 {
		t.Errorf("another session has not verified anything yet: exit %d", code)
	}
	// Edits, and new files, put the tests back in play.
	if err := os.WriteFile(filepath.Join(repo, "marker"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := run("s1", "exit 1"); code != 2 {
		t.Errorf("an edited file needs verifying again: exit %d", code)
	}
	if code, _ := run("s1", "exit 0"); code != 0 {
		t.Fatal("green again")
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _ := run("s1", "exit 1"); code != 2 {
		t.Errorf("a new file needs verifying again: exit %d", code)
	}
	// Long output is cut to the tail, where failures are.
	code, out := run("s3", "i=0; while [ $i -lt 200 ]; do echo line$i; i=$((i+1)); done; exit 1")
	if code != 2 || strings.Contains(out, "line10\n") || !strings.Contains(out, "line199") {
		t.Errorf("only the last 40 lines go to the model: exit %d\n%s", code, out)
	}
}
