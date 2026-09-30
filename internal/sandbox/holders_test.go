package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

// TestHolders covers the placeholders themselves on any OS.
func TestHolders(t *testing.T) {
	root := real(t.TempDir())
	os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755)
	os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n"), 0o644)
	protected := []string{
		filepath.Join(root, ".larik"), filepath.Join(root, ".mcp.json"),
		filepath.Join(root, ".git", "hooks"), filepath.Join(root, ".git", "config"),
		filepath.Join(root, ".git", "commondir"), filepath.Join(root, ".git", "modules"),
		"/outside/the/project/.mcp.json",
	}
	h := newHolders()
	defer h.close()
	read := func(rel string) string {
		data, _ := os.ReadFile(filepath.Join(root, rel))
		return string(data)
	}

	first := h.hold(protected, []string{root})
	var got []string
	for _, p := range first {
		rel, _ := filepath.Rel(root, p)
		if fi, _ := os.Lstat(p); fi != nil && fi.IsDir() {
			rel += "/"
		}
		got = append(got, rel)
	}
	// Existing paths and ones outside the writable project need none.
	if want := []string{".larik/", ".mcp.json", ".git/commondir", ".git/modules/"}; !slices.Equal(got, want) {
		t.Fatalf("placeholders: %v, want %v", got, want)
	}
	// Each is valid for its readers while it's there.
	if read(".mcp.json") != "{}\n" || read(".git/commondir") != ".\n" {
		t.Errorf("placeholder contents: %q %q", read(".mcp.json"), read(".git/commondir"))
	}
	a, b := exec.Command("true"), exec.Command("true")
	h.started(a, first)

	// A second command while the first runs shares them.
	second := h.hold(protected, []string{root})
	if !slices.Equal(second, first) {
		t.Fatalf("a concurrent command needs the same placeholders: %v", second)
	}
	h.started(b, second)

	h.finished(a, false)
	if !exists(filepath.Join(root, ".mcp.json")) {
		t.Error("a placeholder was removed while another command still uses it")
	}
	// Something real put there meanwhile is kept.
	os.WriteFile(filepath.Join(root, ".git", "modules", "real"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, ".git", "commondir"), []byte("../elsewhere\n"), 0o644)
	h.finished(b, false)
	for _, gone := range []string{".larik", ".mcp.json"} {
		if exists(filepath.Join(root, gone)) {
			t.Errorf("%s was left in the project", gone)
		}
	}
	for _, kept := range []string{".git/modules/real", ".git/commondir", ".git/config", ".git/hooks"} {
		if !exists(filepath.Join(root, kept)) {
			t.Errorf("%s should still be there", kept)
		}
	}

	// A command that left a process running keeps its placeholders until close.
	c := exec.Command("true")
	h.started(c, h.hold(protected, []string{root}))
	h.finished(c, true)
	if !exists(filepath.Join(root, ".mcp.json")) {
		t.Error("placeholders of a still-running sandbox must stay")
	}
	h.close()
	if exists(filepath.Join(root, ".mcp.json")) || exists(filepath.Join(root, ".larik")) {
		t.Error("close should remove what's left")
	}

	// A placeholder directory standing in for a missing parent is bound too.
	deep := filepath.Join(root, "sub", "dir", "commondir")
	held := h.hold([]string{deep}, []string{root})
	if len(held) != 1 || held[0] != filepath.Join(root, "sub") {
		t.Fatalf("held for a deep path: %v", held)
	}
	sb := &Sandbox{kind: "bubblewrap", root: root, protected: []string{deep}}
	if args := strings.Join(sb.bwrapArgs("true", root, held...), " "); !strings.Contains(args, "--ro-bind "+held[0]+" "+held[0]) {
		t.Errorf("the placeholder isn't bound read-only: %s", args)
	}
}

// TestMissingProtectedPathsCantBeCreated runs the real Linux sandbox.
func TestMissingProtectedPathsCantBeCreated(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap only; Seatbelt denies by path")
	}
	sb, root, _ := newTest(t, Config{})
	for _, script := range []string{
		"echo x > .mcp.json",
		"mkdir -p .larik && echo x > .larik/settings.json",
		"mkdir -p .claude/commands && echo x > .claude/commands/pwn.md",
		"echo /tmp/x > .git/commondir",
		"echo x > .git/config.worktree",
		"mkdir -p .git/modules/m",
		"mkdir -p .git/worktrees/w",
	} {
		if out, err := run(t, sb, root, script); err == nil {
			t.Errorf("%q must fail: %s", script, out)
		}
	}
	// Nothing is left behind in the project.
	var left []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if rel, _ := filepath.Rel(root, p); rel != "." && rel != ".git" && !strings.HasPrefix(rel, filepath.Join(".git", "hooks")) {
			left = append(left, rel)
		}
		return nil
	})
	if len(left) != 0 {
		t.Errorf("left in the project: %v", left)
	}
	// Ordinary writes still work, with placeholders in place.
	if out, err := run(t, sb, root, "echo ok > file.txt && cat file.txt"); err != nil || strings.TrimSpace(out) != "ok" {
		t.Errorf("project write: %v %s", err, out)
	}

	// While one command runs, another finishing must not lift its protection.
	slow := sb.Command("sleep 1; echo x > .mcp.json", root)
	if err := slow.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, sb, root, "true"); err != nil {
		t.Fatal(err)
	}
	if err := slow.Wait(); err == nil {
		t.Error("the slower command created .mcp.json after another command finished")
	}
	sb.Finished(slow, false)
	if exists(filepath.Join(root, ".mcp.json")) {
		t.Error(".mcp.json was left in the project")
	}
}

// While a sandboxed command runs, the placeholders are in the real .git;
// git run outside the sandbox (the user's, or Larik's own) must still work.
func TestHostGitWorksWithPlaceholders(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git needed")
	}
	root := real(t.TempDir())
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := git("init", "-q"); err != nil {
		t.Fatal(out)
	}
	var protected []string
	for _, name := range gitProtected {
		protected = append(protected, filepath.Join(root, ".git", name))
	}
	h := newHolders()
	held := h.hold(protected, []string{root})
	if len(held) == 0 {
		t.Fatal("a fresh repository should need placeholders")
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0o644)
	for _, args := range [][]string{{"status", "--short"}, {"add", "a.txt"}, {"commit", "-q", "-m", "one"}, {"worktree", "add", "-q", filepath.Join(root, "wt")}} {
		if out, err := git(args...); err != nil {
			t.Errorf("git %v with placeholders in place: %v %s", args, err, out)
		}
	}
	cmd := exec.Command("true")
	h.started(cmd, held)
	h.finished(cmd, false)
	if exists(filepath.Join(root, ".git", "commondir")) {
		t.Error(".git/commondir placeholder was left behind")
	}
	// The worktree git made under the placeholder directory is real now.
	if !exists(filepath.Join(root, ".git", "worktrees", "wt")) {
		t.Error("the worktree's git dir was removed with the placeholder")
	}
	if out, err := git("log", "--oneline"); err != nil || !strings.Contains(out, "one") {
		t.Errorf("git log afterwards: %v %s", err, out)
	}
}
