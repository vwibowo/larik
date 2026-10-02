package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larik/internal/checkpoint"
	"larik/internal/llm"
)

func run(t *testing.T, tool Tool, env *Env, input string) Result {
	t.Helper()
	return tool.Run(context.Background(), env, json.RawMessage(input))
}

func TestEditRequiresReadAndUniqueness(t *testing.T) {
	dir := t.TempDir()
	env := NewEnv(dir)
	path := filepath.Join(dir, "a.txt")
	os.WriteFile(path, []byte("foo\nfoo\nbar\n"), 0o644)

	if r := run(t, Edit{}, env, `{"path":"a.txt","old_string":"bar","new_string":"baz"}`); !r.IsError || !strings.Contains(r.Content, "not been read") {
		t.Fatalf("edit before read should fail: %+v", r)
	}
	run(t, Read{}, env, `{"path":"a.txt"}`)
	if r := run(t, Edit{}, env, `{"path":"a.txt","old_string":"foo","new_string":"x"}`); !r.IsError || !strings.Contains(r.Content, "appears 2 times") {
		t.Fatalf("ambiguous edit should fail: %+v", r)
	}
	if r := run(t, Edit{}, env, `{"path":"a.txt","old_string":"foo","new_string":"x","replace_all":true}`); r.IsError {
		t.Fatal(r.Content)
	}
	if data, _ := os.ReadFile(path); string(data) != "x\nx\nbar\n" {
		t.Fatalf("content = %q", data)
	}

	// External modification invalidates the read.
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(path, []byte("changed\n"), 0o644)
	os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second))
	if r := run(t, Edit{}, env, `{"path":"a.txt","old_string":"changed","new_string":"y"}`); !r.IsError || !strings.Contains(r.Content, "modified since") {
		t.Fatalf("stale edit should fail: %+v", r)
	}
}

func TestWriteCreatesAndCaptures(t *testing.T) {
	dir := t.TempDir()
	env := NewEnv(dir)
	var captured []string
	env.BeforeWrite = func(p string) error { captured = append(captured, p); return nil }
	if r := run(t, Write{}, env, `{"path":"sub/new.go","content":"package x\n"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if len(captured) != 1 || captured[0] != filepath.Join(dir, "sub/new.go") {
		t.Fatalf("captured = %v", captured)
	}
}

func TestWriteBoundaryAndCheckpointFailure(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	env := NewEnv(dir)
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".git/hooks/pre-commit", ".larik/settings.json", "link/outside.txt", "../outside.txt"} {
		if r := run(t, Write{}, env, `{"path":"`+path+`","content":"bad"}`); !r.IsError {
			t.Errorf("write to %s succeeded", path)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "outside.txt")); !os.IsNotExist(err) {
		t.Fatal("write escaped through a symlink")
	}
	env.BeforeWrite = func(string) error { return fmt.Errorf("snapshot unavailable") }
	if r := run(t, Write{}, env, `{"path":"safe.txt","content":"bad"}`); !r.IsError || !strings.Contains(r.Content, "snapshot unavailable") {
		t.Fatalf("checkpoint failure should stop write: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, "safe.txt")); !os.IsNotExist(err) {
		t.Fatal("file was written without a checkpoint")
	}
}

func TestWriteReplacesHardlinkWithoutChangingOutsideFile(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	external := filepath.Join(outside, "external.txt")
	if err := os.WriteFile(external, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.txt")
	if err := os.Link(external, alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	env := NewEnv(dir)
	if r := run(t, Read{}, env, `{"path":"alias.txt"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if r := run(t, Write{}, env, `{"path":"alias.txt","content":"replacement"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if data, _ := os.ReadFile(external); string(data) != "original" {
		t.Fatalf("outside hardlink target changed: %q", data)
	}
	if data, _ := os.ReadFile(alias); string(data) != "replacement" {
		t.Fatalf("project file was not replaced: %q", data)
	}
	if fi, _ := os.Stat(alias); fi.Mode().Perm() != 0o600 {
		t.Fatalf("replacement lost original permissions: %v", fi.Mode().Perm())
	}
}

func TestReadOffsetAndLineNumbers(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "f"), []byte("a\nb\nc\n"), 0o644)
	r := run(t, Read{}, NewEnv(dir), `{"path":"f","offset":2,"limit":1}`)
	if !strings.Contains(r.Content, "     2\tb") || strings.Contains(r.Content, "\tc") {
		t.Fatalf("got %q", r.Content)
	}
}

func TestBash(t *testing.T) {
	env := NewEnv(t.TempDir())
	if r := run(t, Bash{}, env, `{"command":"echo hi; exit 3"}`); !r.IsError || !strings.Contains(r.Content, "hi") || !strings.Contains(r.Content, "exit code 3") {
		t.Fatalf("got %+v", r)
	}
	start := time.Now()
	r := run(t, Bash{}, env, `{"command":"sleep 30 & sleep 30","timeout":1}`)
	if !r.IsError || !strings.Contains(r.Content, "timed out") || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout not enforced: %+v after %s", r, time.Since(start))
	}
}

func TestBashCheckpointPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(path, []byte("before"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := checkpoint.New(filepath.Join(t.TempDir(), "checkpoints"), dir)
	store.BeginTurn()
	env := NewEnv(dir)
	env.BeforeWrite = store.Capture
	r := run(t, Bash{}, env, `{"command":"printf after > file.txt","checkpoint_paths":["file.txt"]}`)
	if r.IsError {
		t.Fatal(r.Content)
	}
	if data, _ := os.ReadFile(path); string(data) != "after" {
		t.Fatalf("bash result = %q", data)
	}
	if _, err := store.Undo(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "before" {
		t.Fatalf("undo result = %q", data)
	}

	for _, input := range []string{
		`{"command":"printf bad > file.txt","checkpoint_paths":["../outside.txt"]}`,
		`{"command":"printf bad > file.txt","checkpoint_paths":[".larik/settings.json"]}`,
	} {
		if r := run(t, Bash{}, env, input); !r.IsError {
			t.Fatalf("unsafe checkpoint path was accepted: %s", input)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != "before" {
		t.Fatalf("rejected command changed file: %q", data)
	}
}

func TestGrepAndGlob(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("package pkg\nfunc Hello() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello world\n"), 0o644)
	env := NewEnv(dir)

	if r := run(t, Glob{}, env, `{"pattern":"**/*.go"}`); !strings.Contains(r.Content, "a.go") || strings.Contains(r.Content, "b.txt") {
		t.Fatalf("glob: %q", r.Content)
	}
	r := run(t, Grep{}, env, `{"pattern":"hello","ignore_case":true,"glob":"*.go"}`)
	if !strings.Contains(r.Content, "a.go:2:") || strings.Contains(r.Content, "b.txt") {
		t.Fatalf("grep: %q", r.Content)
	}
	// The pure-Go fallback must agree.
	lines := newSearchLines(dir)
	err := goGrep(context.Background(), dir, grepInput{Pattern: "hello", IgnoreCase: true, Glob: "*.go"}, lines)
	if out := lines.String(""); err != nil || !strings.HasPrefix(out, filepath.Join("pkg", "a.go")+":2:") || strings.Contains(out, "b.txt") {
		t.Fatalf("goGrep: %q %v", out, err)
	}
	// Results inside the working directory are shown relative to it.
	if r := run(t, Glob{}, env, `{"pattern":"**/*.go"}`); r.Content != filepath.Join("pkg", "a.go")+"\n" {
		t.Fatalf("glob paths should be relative: %q", r.Content)
	}
	if r := run(t, Grep{}, env, `{"pattern":"Hello"}`); !strings.HasPrefix(r.Content, filepath.Join("pkg", "a.go")+":2:") {
		t.Fatalf("grep paths should be relative: %q", r.Content)
	}
}

// TestSearchLinesCap: results stop at the line and byte caps on a line
// boundary, and say that more were left out.
func TestSearchLinesCap(t *testing.T) {
	s := newSearchLines("/w")
	for i := 0; s.add(fmt.Sprintf("/w/f.go:%d:x", i)); i++ {
	}
	out := s.String("more")
	if s.n != maxSearchResults || !strings.HasPrefix(out, "f.go:0:x\n") || !strings.HasSuffix(out, "... (more)\n") {
		t.Fatalf("n=%d out tail %q", s.n, out[max(0, len(out)-40):])
	}
	s = newSearchLines("/w")
	long := strings.Repeat("y", 1000)
	for s.add("/elsewhere/g.go:1:" + long) {
	}
	if out := s.String("more"); len(out) > MaxOutputBytes+20 || !strings.HasPrefix(out, "/elsewhere/g.go:1:") {
		t.Fatalf("byte cap: %d bytes", len(out))
	}
}

func TestTruncate(t *testing.T) {
	s := strings.Repeat("a", 100) + "END"
	out := Truncate(s, 50)
	if !strings.HasSuffix(out, "END") || !strings.Contains(out, "truncated") {
		t.Fatalf("got %q", out)
	}
	if Truncate("short", 50) != "short" {
		t.Fatal("short strings must pass through")
	}
}

// fakeSandbox runs commands normally but marks them as sandboxed.
type fakeSandbox struct{ calls []string }

func (f *fakeSandbox) Command(script, dir string) *exec.Cmd {
	f.calls = append(f.calls, script)
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	return cmd
}

func TestBashSandboxRouting(t *testing.T) {
	env := NewEnv(t.TempDir())
	fs := &fakeSandbox{}
	env.Sandbox = fs

	r := run(t, Bash{}, env, `{"command":"echo 'curl: (6) Could not resolve host: x' >&2; exit 6"}`)
	if len(fs.calls) != 1 || !strings.Contains(r.Content, `"sandbox": false`) {
		t.Fatalf("sandboxed failure should hint at the escape hatch: %+v", r)
	}
	r = run(t, Bash{}, env, `{"command":"echo plain failure; exit 1"}`)
	if strings.Contains(r.Content, "sandbox:") {
		t.Error("unrelated failures get no sandbox hint")
	}
	run(t, Bash{}, env, `{"command":"echo hi","sandbox":false}`)
	if len(fs.calls) != 2 {
		t.Errorf("sandbox:false must bypass the sandbox, calls=%v", fs.calls)
	}
}

// proxiedSandbox is a fakeSandbox whose proxy refused a host.
type proxiedSandbox struct{ fakeSandbox }

func (*proxiedSandbox) NetworkBlocked(since time.Time) []string { return []string{"evil.example"} }

func TestBashReportsHostsTheProxyRefused(t *testing.T) {
	env := NewEnv(t.TempDir())
	env.Sandbox = &proxiedSandbox{}
	if r := run(t, Bash{}, env, `{"command":"echo fetched"}`); !strings.Contains(r.Content, "the network proxy refused evil.example") {
		t.Errorf("sandboxed run: %s", r.Content)
	}
	if r := run(t, Bash{}, env, `{"command":"echo fetched","sandbox":false}`); strings.Contains(r.Content, "proxy refused") {
		t.Errorf("an unsandboxed run has no proxy note: %s", r.Content)
	}
}

// trackingSandbox records which commands it was told have finished.
type trackingSandbox struct {
	fakeSandbox
	finished []bool
}

func (s *trackingSandbox) Finished(_ *exec.Cmd, leftRunning bool) {
	s.finished = append(s.finished, leftRunning)
}

func TestBashTellsTheSandboxWhenACommandEnds(t *testing.T) {
	env := NewEnv(t.TempDir())
	sb := &trackingSandbox{}
	env.Sandbox = sb
	run(t, Bash{}, env, `{"command":"echo one"}`)
	run(t, Bash{}, env, `{"command":"exit 3"}`)
	run(t, Bash{}, env, `{"command":"echo unsandboxed","sandbox":false}`)
	if len(sb.finished) != 2 || sb.finished[0] || sb.finished[1] {
		t.Errorf("want two sandboxed commands reported finished, none left running: %v", sb.finished)
	}
}

// changingTool describes itself differently each time, like the task tool
// after a routing change.
type changingTool struct{ n *int }

func (c changingTool) Spec() llm.ToolSpec {
	*c.n++
	return llm.ToolSpec{Name: "changing", Description: fmt.Sprint("version ", *c.n)}
}
func (changingTool) ReadOnly() bool                                    { return true }
func (changingTool) Run(context.Context, *Env, json.RawMessage) Result { return Result{} }

func TestRegistrySpecsFixedPerContext(t *testing.T) {
	n := 0
	r := NewRegistry(changingTool{&n})
	first := r.Specs()[0].Description
	if again := r.Specs()[0].Description; again != first {
		t.Fatalf("spec changed within one registry: %q then %q", first, again)
	}
}

func TestBashDoesNotWaitForBackgroundProcess(t *testing.T) {
	env := NewEnv(t.TempDir())
	start := time.Now()
	r := run(t, Bash{}, env, `{"command":"sleep 20 & echo started"}`)
	if took := time.Since(start); took > PipeWaitDelay+3*time.Second {
		t.Fatalf("waited %s for a background process holding the output", took)
	}
	if r.IsError || !strings.Contains(r.Content, "started") || !strings.Contains(r.Content, "still running in the background") {
		t.Fatalf("got %+v", r)
	}
}

func TestGlobIntoNamedHiddenDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".github", "workflows"), 0o755)
	os.WriteFile(filepath.Join(dir, ".github", "workflows", "ci.yml"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(dir, "dist"), 0o755)
	os.WriteFile(filepath.Join(dir, "dist", "app.js"), []byte("x"), 0o644)
	env := NewEnv(dir)
	if r := run(t, Glob{}, env, `{"pattern":".github/workflows/*.yml"}`); !strings.Contains(r.Content, "ci.yml") {
		t.Errorf("named hidden dir: %q", r.Content)
	}
	if r := run(t, Glob{}, env, `{"pattern":"dist/**"}`); !strings.Contains(r.Content, "app.js") {
		t.Errorf("named build dir: %q", r.Content)
	}
	if r := run(t, Glob{}, env, `{"pattern":"**/*.yml"}`); strings.Contains(r.Content, "ci.yml") {
		t.Errorf("an unnamed hidden dir should still be skipped: %q", r.Content)
	}
}

func TestMultiEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	os.WriteFile(path, []byte("func old() {}\nfunc caller() { old() }\nvar x = 1\nvar y = 1\n"), 0o644)
	env := NewEnv(dir)
	if r := run(t, MultiEdit{}, env, `{"path":"a.go","edits":[{"old_string":"x","new_string":"z"}]}`); !r.IsError || !strings.Contains(r.Content, "not been read") {
		t.Fatalf("must read first: %+v", r)
	}
	run(t, Read{}, env, `{"path":"a.go"}`)

	// The second edit fails, so the first must not be written either.
	r := run(t, MultiEdit{}, env, `{"path":"a.go","edits":[{"old_string":"var x","new_string":"var z"},{"old_string":"missing","new_string":"m"}]}`)
	if !r.IsError || !strings.Contains(r.Content, "edit 2 of 2") || !strings.Contains(r.Content, "nothing was changed") {
		t.Fatalf("failing edit: %+v", r)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), "var z") {
		t.Fatal("a failed multi_edit must leave the file unchanged")
	}

	// Edits apply in order, each to the result of the ones before.
	r = run(t, MultiEdit{}, env, `{"path":"a.go","edits":[
		{"old_string":"old","new_string":"renamed","replace_all":true},
		{"old_string":"func renamed() {}","new_string":"func renamed() { println() }"},
		{"old_string":"= 1","new_string":"= 2","replace_all":true}]}`)
	if r.IsError || !strings.Contains(r.Content, "3 edits, 5 replacement(s)") || !strings.Contains(r.Display, "⋯") {
		t.Fatalf("multi_edit: %+v", r)
	}
	data, _ := os.ReadFile(path)
	if want := "func renamed() { println() }\nfunc caller() { renamed() }\nvar x = 2\nvar y = 2\n"; string(data) != want {
		t.Fatalf("file = %q", data)
	}
	for input, want := range map[string]string{
		`{"path":"a.go","edits":[]}`:                                        "edits is empty",
		`{"path":"a.go","edits":[{"old_string":"var","new_string":"let"}]}`: "appears 2 times",
		`{"path":"a.go","edits":[{"old_string":"x","new_string":"x"}]}`:     "identical",
	} {
		if r := run(t, MultiEdit{}, env, input); !r.IsError || !strings.Contains(r.Content, want) {
			t.Errorf("%s: %+v, want %q", input, r, want)
		}
	}
}

// TestReadStopsAtBudget: a file too large for one read is cut on a line
// boundary, and the hint names the first line that wasn't shown.
func TestReadStopsAtBudget(t *testing.T) {
	dir := t.TempDir()
	var src strings.Builder
	for i := 1; i <= 1500; i++ {
		fmt.Fprintf(&src, "line %04d %s\n", i, strings.Repeat("x", 80))
	}
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(src.String()), 0o644)
	r := run(t, Read{}, NewEnv(dir), `{"path":"big.txt"}`)
	if strings.Contains(r.Content, "truncated") {
		t.Fatal("read must not cut the middle out")
	}
	var last int
	for _, l := range strings.Split(strings.TrimSpace(r.Content), "\n") {
		fmt.Sscanf(strings.TrimSpace(l), "%d", &last)
	}
	want := fmt.Sprintf("continue with offset=%d)", last+1)
	if !strings.HasSuffix(strings.TrimSpace(r.Content), want) || len(r.Content) > readOutputBytes+100 {
		t.Fatalf("got %d bytes ending %q, want hint %q", len(r.Content), r.Content[len(r.Content)-60:], want)
	}
}

// TestGlobFollowsGitignore: with ripgrep, glob leaves out what .gitignore
// excludes, yet a pattern aimed at an ignored directory still finds it.
func TestGlobFollowsGitignore(t *testing.T) {
	if ripgrepPath() == "" {
		t.Skip("ripgrep not installed")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for name, content := range map[string]string{".gitignore": "out/\n", "src/a.js": "a", "out/b.js": "b"} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
	}
	env := NewEnv(dir)
	if r := run(t, Glob{}, env, `{"pattern":"**/*.js"}`); r.Content != filepath.Join("src", "a.js")+"\n" {
		t.Fatalf("ignored files should be left out: %q", r.Content)
	}
	if r := run(t, Glob{}, env, `{"pattern":"out/*.js"}`); r.Content != filepath.Join("out", "b.js")+"\n" {
		t.Fatalf("a pattern naming an ignored directory should still find it: %q", r.Content)
	}
}

type nopCaller struct{}

func (nopCaller) CallTool(context.Context, string, json.RawMessage) Result { return Result{} }

// TestRepeatedReadIsShort: reading the same unchanged lines again answers
// in a line, but not once the file changed, the record was forgotten (a
// new context), or for a script's reads, which the model never saw.
func TestRepeatedReadIsShort(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	os.WriteFile(path, []byte("package a\n"), 0o644)
	env := NewEnv(dir)
	full := func(r Result) bool { return strings.Contains(r.Content, "package a") }

	if r := run(t, Read{}, env, `{"path":"a.go"}`); !full(r) {
		t.Fatalf("first read: %q", r.Content)
	}
	if r := run(t, Read{}, env, `{"path":"a.go"}`); full(r) || !strings.Contains(r.Content, "unchanged") {
		t.Fatalf("second read should be short: %q", r.Content)
	}
	if r := run(t, Read{}, env, `{"path":"a.go","offset":1,"limit":5}`); !full(r) {
		t.Fatalf("another range is another read: %q", r.Content)
	}
	if err := run(t, Edit{}, env, `{"path":"a.go","old_string":"package a","new_string":"package a // changed"}`); err.IsError {
		t.Fatal(err.Content)
	}
	if r := run(t, Read{}, env, `{"path":"a.go"}`); !full(r) {
		t.Fatalf("a changed file is read in full: %q", r.Content)
	}
	env.ForgetShown()
	if r := run(t, Read{}, env, `{"path":"a.go"}`); !full(r) {
		t.Fatalf("after ForgetShown the read is full: %q", r.Content)
	}
	script := WithCaller(context.Background(), nopCaller{})
	if r := (Read{}).Run(script, env, json.RawMessage(`{"path":"a.go"}`)); !full(r) {
		t.Fatalf("a script's read gets the content: %q", r.Content)
	}
	env.NoReadDedup.Store(true)
	if r := run(t, Read{}, env, `{"path":"a.go"}`); !full(r) {
		t.Fatalf("with dedup off every read is full: %q", r.Content)
	}
}
