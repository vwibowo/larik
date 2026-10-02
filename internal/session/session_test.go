package session

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"larik/internal/llm"
)

func text(role llm.Role, s string) llm.Message {
	return llm.Message{Role: role, Blocks: []llm.Block{{Type: llm.BlockText, Text: s}}}
}

func TestProjectDirectoriesDoNotCollide(t *testing.T) {
	base, data := t.TempDir(), t.TempDir()
	a, b := filepath.Join(base, "a", "b-c"), filepath.Join(base, "a-b", "c")
	for _, dir := range []string{a, b} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if Dir(data, a) == Dir(data, b) {
		t.Fatal("project directory identity is not collision resistant")
	}
	for _, cwd := range []string{a, b} {
		s, err := Create(Dir(data, cwd), Meta{Cwd: cwd, Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		s.Close()
	}
	if got, err := List(Dir(data, a)); err != nil || len(got) != 1 {
		t.Fatalf("project a sessions: %v %v", got, err)
	}
	if got, err := List(Dir(data, b)); err != nil || len(got) != 1 {
		t.Fatalf("project b sessions: %v %v", got, err)
	}
}

func TestSessionWriterLockAcrossProcesses(t *testing.T) {
	s, err := Create(t.TempDir(), Meta{Cwd: t.TempDir(), Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	probe := func() string {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestSessionLockHelper$")
		cmd.Env = append(os.Environ(), "LARIK_LOCK_TEST_PATH="+s.Path)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("lock helper: %v: %s", err, out)
		}
		return string(out)
	}
	if got := probe(); !strings.Contains(got, "already open for writing") {
		t.Fatalf("second process did not see a locked session: %s", got)
	}
	if _, err := Load(s.Path); err != nil {
		t.Fatalf("read-only load should work while locked: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if got := probe(); !strings.Contains(got, "opened") {
		t.Fatalf("lock was not released on close: %s", got)
	}
}

func TestSessionLockHelper(t *testing.T) {
	path := os.Getenv("LARIK_LOCK_TEST_PATH")
	if path == "" {
		return
	}
	s, _, err := Open(path)
	if err != nil {
		fmt.Println(err)
		return
	}
	s.Close()
	fmt.Println("opened")
}

// transcript: two turns, the first with a tool call.
func seed(t *testing.T, dir string) *Session {
	t.Helper()
	s, err := Create(dir, Meta{Cwd: "/w", Provider: "fake", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	u := &llm.Usage{Input: 100, Output: 10}
	s.AppendMessage(text(llm.RoleUser, "first prompt"), nil)
	s.AppendMessage(llm.Message{Role: llm.RoleAssistant, Model: "m", Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "read", Input: json.RawMessage(`{}`)}}}, u)
	s.AppendMessage(llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: "t1", Content: "ok"}, {Type: llm.BlockText, Text: "<note>"}}}, nil)
	s.AppendMessage(text(llm.RoleAssistant, "answer one"), u)
	s.AppendUsage("m", llm.Usage{Input: 5})
	s.AppendCompaction("summary of turn one")
	s.AppendMessage(text(llm.RoleUser, "second prompt"), nil)
	s.AppendMessage(text(llm.RoleAssistant, "answer two"), u)
	return s
}

func TestPrompts(t *testing.T) {
	s := seed(t, t.TempDir())
	s.Close()
	st, _ := Load(s.Path)
	// Index 2 has text but also a tool result: not a turn boundary.
	if got := Prompts(st.All); !slices.Equal(got, []int{0, 4}) {
		t.Errorf("prompts: %v", got)
	}
}

func TestFork(t *testing.T) {
	dir := t.TempDir()
	src := seed(t, dir)
	defer src.Close()

	// Whole transcript.
	b, st, err := Fork(dir, src.Path, -1)
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	if len(st.All) != 6 || st.Meta.ForkOf != src.ID || st.Meta.ForkAt != 6 || st.Meta.Model != "m" {
		t.Errorf("full fork: %d messages, meta %+v", len(st.All), st.Meta)
	}
	// The compaction is carried over: the active context is the summary
	// (merged with the next prompt) + the answer.
	if len(st.Messages) != 2 || !strings.Contains(st.Messages[0].Text(), "summary of turn one") || st.Messages[1].Text() != "answer two" {
		t.Errorf("active context: %+v", st.Messages)
	}
	if st.Usage != (llm.Usage{}) || st.Cost != 0 {
		t.Errorf("usage must not be copied: %+v %v", st.Usage, st.Cost)
	}

	// Before the second prompt: turn one, plus the compaction that came
	// before that prompt, so the branch resumes from the same context.
	b2, st, err := Fork(dir, src.Path, 4)
	if err != nil {
		t.Fatal(err)
	}
	b2.Close()
	if len(st.All) != 4 || len(st.Messages) != 1 || st.All[3].Text() != "answer one" {
		t.Errorf("fork at 4: all=%d active=%d", len(st.All), len(st.Messages))
	}
	// Branches get their own ids, and appending to one leaves the source alone.
	b3, _, _ := Fork(dir, src.Path, 0)
	b3.AppendMessage(text(llm.RoleUser, "different start"), nil)
	b3.Close()
	if orig, _ := Load(src.Path); len(orig.All) != 6 {
		t.Errorf("source changed: %d", len(orig.All))
	}

	for _, bad := range []int{1, 2, 3, 7} {
		if _, _, err := Fork(dir, src.Path, bad); err == nil {
			t.Errorf("fork at %d should fail", bad)
		}
	}

	infos, _ := List(dir)
	forks := 0
	for _, in := range infos {
		if in.ForkOf == src.ID {
			forks++
		}
		if in.ID == b3.ID && in.Title != "different start" {
			t.Errorf("branch title: %q", in.Title)
		}
	}
	if len(infos) != 4 || forks != 3 {
		t.Errorf("list: %+v", infos)
	}
}

func TestForkCarriesOnlyKeptRawOutput(t *testing.T) {
	dir := t.TempDir()
	s, err := Create(dir, Meta{Cwd: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"first", "second"} {
		s.AppendMessage(text(llm.RoleUser, "prompt "+id), nil)
		s.AppendMessage(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: "bash"}}}, nil)
		s.AppendMessage(llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ID: id, Name: "bash", Content: "short"}}}, nil)
		if err := os.MkdirAll(RawDir(s.Path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(RawPath(s.Path, id), []byte("exact "+id), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fork, _, err := Fork(dir, s.Path, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer fork.Close()
	if data, err := os.ReadFile(RawPath(fork.Path, "first")); err != nil || string(data) != "exact first" {
		t.Fatalf("kept raw: %q %v", data, err)
	}
	if _, err := os.Stat(RawPath(fork.Path, "second")); !os.IsNotExist(err) {
		t.Fatalf("discarded raw copied: %v", err)
	}
}

func TestUsageByModel(t *testing.T) {
	s, err := Create(t.TempDir(), Meta{Model: "big"})
	if err != nil {
		t.Fatal(err)
	}
	s.AppendMessage(llm.Message{Role: llm.RoleAssistant, Model: "big", Blocks: []llm.Block{llm.TextBlock("a")}}, &llm.Usage{Input: 10})
	s.AppendUsage("small", llm.Usage{Input: 5})
	s.AppendUsage("small", llm.Usage{Output: 2})
	s.Close()
	st, err := Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if st.ByModel["big"].Input != 10 || st.ByModel["small"].Input != 5 || st.ByModel["small"].Output != 2 || st.Usage.Input != 15 {
		t.Errorf("by model = %+v, total %+v", st.ByModel, st.Usage)
	}
}

func TestTornLastLineDoesNotEatNextEntry(t *testing.T) {
	s, err := Create(t.TempDir(), Meta{Cwd: "/w", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	s.AppendMessage(llm.UserText("first"), nil)
	s.Close()
	f, _ := os.OpenFile(s.Path, os.O_WRONLY|os.O_APPEND, 0o600)
	f.WriteString(`{"type":"message","mess`) // a crash mid-write
	f.Close()

	s2, st, err := Open(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Messages) != 1 {
		t.Fatalf("messages before the torn line: %d", len(st.Messages))
	}
	s2.AppendMessage(llm.UserText("second"), nil)
	s2.Close()
	st, err = Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(st.Messages); n != 1 || !strings.Contains(st.Messages[0].Text(), "second") {
		t.Fatalf("the entry after a torn line must survive: %+v", st.Messages)
	}
}

func TestGeneratedMessagesAreNotPrompts(t *testing.T) {
	msgs := []llm.Message{
		text(llm.RoleUser, "fix the bug"),
		text(llm.RoleUser, "<task-notification id=\"bg1\" agent=\"a\" status=\"done\">\nok\n</task-notification>"),
		text(llm.RoleUser, "<hook-feedback source=\"Stop\">\nrun the tests\n</hook-feedback>"),
		text(llm.RoleUser, "<system-note>\nfiles changed\n</system-note>\n\nnext step"),
	}
	if got := Prompts(msgs); !slices.Equal(got, []int{0, 3}) {
		t.Fatalf("prompts = %v, want [0 3]", got)
	}
}

// TestEntriesAreGreppable: the transcript keeps <, > and & literal, since
// the model searches it with grep after compaction.
func TestEntriesAreGreppable(t *testing.T) {
	dir := t.TempDir()
	s, err := Create(dir, Meta{Cwd: dir, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.AppendMessage(llm.UserText("go test ./... && echo <ok> -> done"), nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "go test ./... && echo <ok> -> done") {
		t.Fatalf("transcript escapes the text: %s", data)
	}
}
