package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/tools"
)

func TestCredentialReadDeniedInEveryExecutionMode(t *testing.T) {
	for _, mode := range []tools.Execution{tools.ExecTools, tools.ExecHybrid, tools.ExecCode} {
		t.Run(string(mode), func(t *testing.T) {
			dir := t.TempDir()
			secret := filepath.Join(dir, "auth.json")
			if err := os.WriteFile(secret, []byte("PRIVATE_TOKEN"), 0600); err != nil {
				t.Fatal(err)
			}
			input, _ := json.Marshal(map[string]string{"path": secret})
			call := toolUse("read-secret", "read", string(input))
			if mode != tools.ExecTools {
				code := `try { console.log(tools.read(` + string(input) + `)); } catch(e) { console.log(String(e)); }`
				script, _ := json.Marshal(map[string]string{"code": code})
				call = toolUse("script", "run_code", string(script))
			}
			provider := &fakeProvider{script: []llm.Message{assistant(call), assistant(llm.TextBlock("done"))}}
			a := New(Options{Provider: provider, Model: "m", Cwd: dir, Tools: tools.Default(), Execution: mode, SecretPaths: []string{secret}, Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{}, dir)})
			denied := false
			for _, e := range drain(a.Run(context.Background(), "read the file"), PermissionReply{}) {
				if strings.Contains(e.Output, "PRIVATE_TOKEN") {
					t.Fatal("credential leaked")
				}
				if e.Kind == EvToolEnd && e.ToolName == "read" && e.IsError {
					denied = true
				}
			}
			if !denied {
				t.Fatal("read did not execute and deny credential access")
			}
		})
	}
}
