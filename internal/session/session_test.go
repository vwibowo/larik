package session

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"larik/internal/llm"
)

func text(role llm.Role, s string) llm.Message {
	return llm.Message{Role: role, Blocks: []llm.Block{{Type: llm.BlockText, Text: s}}}
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
