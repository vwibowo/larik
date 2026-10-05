package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonalHooksPatchPreservesApprovalAndUnknownFields(t *testing.T) {
	c := &Config{ConfigDir: t.TempDir()}
	path := c.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := `{"theme":"dark","approved_project_hooks":"pinned","hooks":{"FutureEvent":[{"hooks":[{"command":"future"}]}],"Stop":[{"matcher":"x","opaque":true,"hooks":[{"type":"command","command":"old","extra":3}]}]}}`
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	events, err := PersonalHooksAt(path)
	if err != nil {
		t.Fatal(err)
	}
	h := events["Stop"][0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	h["command"] = "new"
	if err := c.PatchUserHooks(map[string][]any{"Stop": events["Stop"]}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{`"approved_project_hooks": "pinned"`, `"FutureEvent"`, `"opaque": true`, `"extra": 3`, `"command": "new"`, `"theme": "dark"`} {
		if !strings.Contains(string(b), s) {
			t.Errorf("missing %s: %s", s, b)
		}
	}
	if err := c.PatchUserHooks(map[string][]any{"Stop": nil}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), `"Stop"`) || !strings.Contains(string(b), `"FutureEvent"`) {
		t.Fatalf("delete: %s", b)
	}
	if err := c.PatchUserHooks(map[string][]any{"FutureEvent": nil}); err == nil {
		t.Fatal("accepted unsupported event")
	}
}
