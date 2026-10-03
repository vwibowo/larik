package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitTest(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	} else {
		return strings.TrimSpace(string(out))
	}
	return ""
}

func TestReadProjectInfoCleanAndChanged(t *testing.T) {
	dir := t.TempDir()
	gitTest(t, dir, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, dir, "add", "app.go")
	gitTest(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-m", "initial")
	clean := readProjectInfo(dir)
	if !clean.available || !clean.clean || clean.branch != "main" {
		t.Fatalf("clean project = %+v", clean)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n\nfunc Run() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	changed := readProjectInfo(dir)
	if !changed.available || changed.clean || changed.files != 2 || changed.added != 2 {
		t.Fatalf("changed project = %+v", changed)
	}
}

func TestReadProjectInfoOutsideGit(t *testing.T) {
	p := readProjectInfo(t.TempDir())
	if p.available || p.err == "" {
		t.Fatalf("non-repository state = %+v", p)
	}
}
