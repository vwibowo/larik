package llm

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestRepairToolHistoryFillsMidHistoryOrphans(t *testing.T) {
	messages := []Message{
		UserText("start"),
		{Role: RoleAssistant, Model: "m", Blocks: []Block{
			{Type: BlockToolUse, ID: "call_1", Name: "read", Input: json.RawMessage(`{"path":"a"}`)},
			{Type: BlockToolUse, ID: "call_2", Name: "grep", Input: json.RawMessage(`{"pattern":"x"}`)},
		}},
		{Role: RoleUser, Blocks: []Block{
			{Type: BlockToolResult, ID: "call_2", Name: "grep", Content: "found"},
			TextBlock("continue"),
		}},
		{Role: RoleAssistant, Model: "m", Blocks: []Block{TextBlock("done")}},
		UserText("next"),
	}
	before, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}

	got := RepairToolHistory(messages)
	if len(got) != len(messages) {
		t.Fatalf("got %d messages, want %d", len(got), len(messages))
	}
	blocks := got[2].Blocks
	if len(blocks) != 3 {
		t.Fatalf("result blocks = %+v", blocks)
	}
	if blocks[0].Type != BlockToolResult || blocks[0].ID != "call_1" || blocks[0].Name != "read" || !blocks[0].IsError {
		t.Errorf("synthetic result = %+v", blocks[0])
	}
	if blocks[1].Type != BlockToolResult || blocks[1].ID != "call_2" || blocks[1].Content != "found" {
		t.Errorf("existing result = %+v", blocks[1])
	}
	if blocks[2].Type != BlockText || blocks[2].Text != "continue" {
		t.Errorf("user content = %+v", blocks[2])
	}
	after, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("repair mutated the stored messages")
	}
}

func TestRepairToolHistoryInsertsResultMessage(t *testing.T) {
	messages := []Message{
		UserText("start"),
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolUse, ID: "call_1", Name: "bash"}}},
	}
	got := RepairToolHistory(messages)
	if len(got) != 3 || got[2].Role != RoleUser || len(got[2].Blocks) != 1 {
		t.Fatalf("repaired history = %+v", got)
	}
	if result := got[2].Blocks[0]; result.Type != BlockToolResult || result.ID != "call_1" || !result.IsError {
		t.Fatalf("synthetic result = %+v", result)
	}
}

func TestRepairToolHistoryLeavesValidHistoryEquivalent(t *testing.T) {
	messages := []Message{
		UserText("start"),
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolUse, ID: "call_1", Name: "read"}}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ID: "call_1", Name: "read", Content: "ok"}, TextBlock("continue")}},
	}
	if got := RepairToolHistory(messages); !reflect.DeepEqual(got, messages) {
		t.Fatalf("got %+v, want %+v", got, messages)
	}
}
