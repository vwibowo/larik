package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"larik/internal/session"
)

type shortWriter struct{}

func (shortWriter) Write(p []byte) (int, error) { return min(2, len(p)), errors.New("disk full") }

func TestLockedWriterKeepsCommandOutputFlowOnCaptureFailure(t *testing.T) {
	var fallback bytes.Buffer
	w := &lockedWriter{w: shortWriter{}, fallback: &fallback}
	if n, err := w.Write([]byte("abcdef")); n != 6 || err != nil {
		t.Fatalf("first write: %d %v", n, err)
	}
	if n, err := w.Write([]byte("ghij")); n != 4 || err != nil {
		t.Fatalf("second write: %d %v", n, err)
	}
	if w.err == nil || fallback.String() != "cdefghij" {
		t.Fatalf("fallback: %q %v", fallback.String(), w.err)
	}
}

func TestTokenSaverRecognizedFormats(t *testing.T) {
	cases := []struct{ command, raw, want string }{
		{"git status", "On branch main\n\nChanges not staged for commit:\n  (use \"git add\" to update)\n\tmodified: a.go\n", "unstaged modified (1):\n  a.go"},
		{"git log -n 2", "commit abcdef\nAuthor: A <a@b>\nDate: today\n\n    first\n\ncommit 123456\nAuthor: B <b@c>\nDate: yesterday\n\n    second\n", "abcdef A <a@b> first"},
		{"git diff --stat", "\n a.go | 2 ++\n\n 1 file changed\n", "a.go | 2 ++"},
		{"rg needle .", "a.go:2:needle\na.go:8:needle again\n", "a.go:\n  2:needle"},
		{"find . -type f", "./a\n./b\n", "./\n  a\n  b"},
		{"ls -l", "total 2\n-rw-r--r-- a\n", "-rw-r--r-- a"},
		{"go test ./...", "ok  a/pkg 0.1s\nok  b/pkg 0.2s\n", "PASS 2 Go package(s)"},
		{"cargo test", "test one ... ok\ntest two ... ok\ntest result: ok. 2 passed\n", "PASS 2 Cargo test(s)"},
		{"cargo build", "   Compiling foo v0.1\n   Compiling bar v0.1\n    Finished dev profile\n", "Compiled 2 crate(s)"},
		{"npm test", "✓ one\n✓ two\nTests: 2 passed\n", "PASS 2 test(s)"},
		{"pytest", "tests/test_a.py .... [100%]\n================ 2 passed in 0.1s ================\n", "2 passed in 0.1s"},
	}
	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got, ok := filterCommandOutput(tc.command, tc.raw)
			if !ok || !strings.Contains(got, tc.want) {
				t.Fatalf("got %q, ok=%v; want %q", got, ok, tc.want)
			}
		})
	}
	for _, command := range []string{"git diff", "go test ./... | cat", "unknown --verbose"} {
		if got, ok := filterCommandOutput(command, "patch or unfamiliar output\n"); ok {
			t.Errorf("%q unexpectedly filtered: %q", command, got)
		}
	}
}

func TestGitStatusKeepsStagesAndPaths(t *testing.T) {
	raw := "On branch main\nChanges to be committed:\n\tnew file:   staged.go\nChanges not staged for commit:\n\tmodified:   edited.go\n\tdeleted:    removed.go\nUntracked files:\n\tnew.go\n"
	got, ok := filterCommandOutput("git status", raw)
	if !ok { t.Fatal("standard status was not recognized") }
	for _, want := range []string{"staged new file (1):\n  staged.go", "unstaged modified (1):\n  edited.go", "unstaged deleted (1):\n  removed.go", "untracked files (1):\n  new.go"} {
		if !strings.Contains(got, want) { t.Fatalf("missing %q in %q", want, got) }
	}
	if _, ok := filterCommandOutput("git status", "On branch main\n\tunexpected: no section\n"); ok { t.Fatal("unfamiliar status shape was filtered") }
}

func TestTokenSaverBashRawRecoveryAndBypass(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	var lines strings.Builder
	for i := 0; i < 80; i++ {
		lines.WriteString("ok  package/with/quite/a/long/path/name " + strings.Repeat("x", 30) + "\n")
	}
	raw := lines.String()
	script := "#!/bin/sh\ncat <<'OUT'\n" + raw + "OUT\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	sess, err := session.Create(dir, session.Meta{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	env := NewEnv(dir)
	env.RawOutputDir = session.RawDir(sess.Path)
	env.TokenSaver = &atomic.Bool{}
	env.TokenSaver.Store(true)
	call := func(id string, rawOutput bool) Result {
		input, _ := json.Marshal(map[string]any{"command": "go test ./...", "raw_output": rawOutput})
		return (Bash{}).Run(WithCallID(context.Background(), id), env, input)
	}
	filtered := call("call-filtered", false)
	if filtered.IsError || !strings.Contains(filtered.Content, "PASS 80 Go package(s)") || !strings.Contains(filtered.Content, "raw_output tool_call_id=call-filtered") {
		t.Fatalf("filtered: %+v", filtered)
	}
	if len(filtered.Content) >= len(raw) {
		t.Fatal("filter did not save context bytes")
	}
	recovered := (RawOutput{}).Run(context.Background(), env, json.RawMessage(`{"tool_call_id":"call-filtered","offset":0,"limit":20000}`))
	if recovered.IsError || !strings.Contains(recovered.Content, raw) {
		t.Fatalf("raw recovery: %+v", recovered)
	}
	fi, err := os.Stat(session.RawPath(sess.Path, "call-filtered"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("raw file permissions: %v %v", fi, err)
	}
	if dirInfo, err := os.Stat(env.RawOutputDir); err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("raw directory permissions: %v %v", dirInfo, err)
	}
	full := call("call-raw", true)
	if full.IsError || full.Content != raw {
		t.Fatalf("bypass: %+v", full)
	}
	env.TokenSaver.Store(false)
	plain := call("call-disabled", false)
	if plain.IsError || plain.Content != raw {
		t.Fatalf("disabled: %+v", plain)
	}
	if _, err := os.Stat(session.RawPath(sess.Path, "call-disabled")); !os.IsNotExist(err) {
		t.Fatalf("disabled call created sidecar: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\necho 'important failure: file.go:12'\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	env.TokenSaver.Store(true)
	failure := call("call-failed", false)
	if !failure.IsError || !strings.Contains(failure.Content, "important failure: file.go:12") || !strings.Contains(failure.Content, "[exit code 3]") {
		t.Fatalf("failure was changed: %+v", failure)
	}
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\ndd if=/dev/zero bs=1000000 count=5 2>/dev/null\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	env.TokenSaver.Store(true)
	large := call("call-large", false)
	if large.IsError || len(large.Content) > MaxOutputBytes+200 {
		t.Fatalf("large output was not bounded: %d bytes, error=%v", len(large.Content), large.IsError)
	}
	fi, err = os.Stat(session.RawPath(sess.Path, "call-large"))
	if err != nil || fi.Size() != 5_000_000 {
		t.Fatalf("large exact output: %v %v", fi, err)
	}
	chunk := (RawOutput{}).Run(context.Background(), env, json.RawMessage(`{"tool_call_id":"call-large","limit":128}`))
	if chunk.IsError || !strings.Contains(chunk.Content, "(base64)") || !strings.Contains(chunk.Content, "next_offset=128") {
		t.Fatalf("large raw chunk: %+v", chunk)
	}
	blockedDir := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(blockedDir, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	env.RawOutputDir = blockedDir
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\necho ran-anyway\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fallback := call("call-fallback", false)
	if fallback.IsError || !strings.Contains(fallback.Content, "ran-anyway") || !strings.Contains(fallback.Content, "token saver unavailable") {
		t.Fatalf("capture failure changed command execution: %+v", fallback)
	}
	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, _, err := session.Open(sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	env.RawOutputDir = session.RawDir(resumed.Path)
	if recovered := (RawOutput{}).Run(context.Background(), env, json.RawMessage(`{"tool_call_id":"call-filtered"}`)); recovered.IsError || !strings.Contains(recovered.Content, raw) {
		t.Fatalf("raw output after resume: %+v", recovered)
	}
}
