package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/tools"
)

func TestCredentialPolicyInheritedBySpawn(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(secret, []byte("PRIVATE_TOKEN"), 0600); err != nil {
		t.Fatal(err)
	}
	a := New(Options{Cwd: dir, SecretPaths: []string{secret}, SecretEnv: []string{"CUSTOM_SECRET"}, SanitizeEnv: func(env []string) []string { return []string{"FILTERED=1"} }})
	child := a.Spawn(SpawnOptions{Type: "general", Tools: tools.NewRegistry()})
	for _, ag := range []*Agent{a, child} {
		input, _ := json.Marshal(map[string]string{"path": secret})
		result := (tools.Read{}).Run(context.Background(), ag.env, input)
		if !result.IsError || strings.Contains(result.Content, "PRIVATE_TOKEN") {
			t.Fatalf("credential exposed: %+v", result)
		}
		if len(ag.env.SecretEnv) != 1 || ag.env.SanitizeEnv(nil)[0] != "FILTERED=1" {
			t.Fatal("environment policy not inherited")
		}
	}
}
