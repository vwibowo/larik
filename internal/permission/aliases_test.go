package permission

import (
	"encoding/json"
	"testing"
)

func TestDocumentedWriteAliasesHonorRules(t *testing.T) {
	for _, tc := range []struct{ tool, input, rule string }{
		{"multi_edit", `{"path":"private/a.go"}`, "edit(private/**)"},
		{"apply_code_action", `{"path":"private/a.go","line":1,"title":"fix"}`, "edit(private/**)"},
		{"browser_screenshot", `{"save_path":"private/a.png"}`, "write(private/**)"},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			dir := t.TempDir()
			c := NewChecker(ModeYolo, Rules{Deny: []string{tc.rule}}, dir)
			if got, _ := c.Decide(Call{Tool: tc.tool, Input: json.RawMessage(tc.input)}); got != Deny {
				t.Fatalf("denial bypassed: %v", got)
			}
			c = NewChecker(ModeDefault, Rules{Allow: []string{tc.rule}}, dir)
			if got, _ := c.Decide(Call{Tool: tc.tool, Input: json.RawMessage(tc.input)}); got != Allow {
				t.Fatalf("alias allow ignored: %v", got)
			}
		})
	}
	c := NewChecker(ModeDefault, Rules{Deny: []string{"write"}}, t.TempDir())
	if got, _ := c.Decide(Call{Tool: "browser_screenshot", Input: json.RawMessage(`{}`), ReadOnly: true}); got != Allow {
		t.Fatalf("capture-only screenshot treated as a write: %v", got)
	}
}

func TestUnconfinedFileReadersRequireApproval(t *testing.T) {
	c := NewChecker(ModeDefault, Rules{}, t.TempDir())
	for _, command := range []string{"cat /private/auth.json", "head /private/auth.json", "tail /private/auth.json", "wc --files0-from=/private/auth.json", "du --files0-from=/private/auth.json"} {
		raw, _ := json.Marshal(map[string]any{"command": command})
		if got, _ := c.Decide(Call{Tool: "bash", Input: raw}); got != Ask {
			t.Fatalf("unconfined credential reader auto-allowed: %s (%v)", command, got)
		}
	}
}
