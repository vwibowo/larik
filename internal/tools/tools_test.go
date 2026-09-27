package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	env.BeforeWrite = func(p string) { captured = append(captured, p) }
	if r := run(t, Write{}, env, `{"path":"sub/new.go","content":"package x\n"}`); r.IsError {
		t.Fatal(r.Content)
	}
	if len(captured) != 1 || captured[0] != filepath.Join(dir, "sub/new.go") {
		t.Fatalf("captured = %v", captured)
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
	out, err := goGrep(context.Background(), dir, grepInput{Pattern: "hello", IgnoreCase: true, Glob: "*.go"})
	if err != nil || !strings.Contains(out, "a.go:2:") || strings.Contains(out, "b.txt") {
		t.Fatalf("goGrep: %q %v", out, err)
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
