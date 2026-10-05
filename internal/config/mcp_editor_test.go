package config

import (
	"os"
	"strings"
	"testing"
)

func TestPatchUserMCPPreservesUnknownAndSecuresFile(t *testing.T) {
	c, path := userConfig(t, `{"theme":"dark","approved_mcp_servers":{"project":"hash"},"mcp_servers":{"personal":{"type":"http","url":"https://old","headers":{"Keep":"secret","Remove":"old"},"oauth":{"client_secret":"hidden","future":7},"future":{"nested":true}},"other":{"command":"other"}}}`)
	servers, err := MCPAt(path)
	if err != nil || len(servers) != 2 {
		t.Fatalf("read: %v, %v", servers, err)
	}
	if err := c.PatchUserMCP(map[string]map[string]any{
		"personal": {"disabled": true, "headers:Remove": nil, "headers:New": "replacement", "oauth:client_secret": nil},
		"new":      {"type": "stdio", "command": "npx", "args": []string{"--yes", "server"}},
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{`"theme": "dark"`, `"project": "hash"`, `"nested": true`, `"future": 7`, `"Keep": "secret"`, `"New": "replacement"`, `"disabled": true`, `"command": "npx"`, `"other": {`} {
		if !strings.Contains(text, want) {
			t.Errorf("lost %s: %s", want, text)
		}
	}
	if strings.Contains(text, `"Remove"`) || strings.Contains(text, `"hidden"`) {
		t.Fatal("cleared secret retained")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	if err := c.PatchUserMCP(map[string]map[string]any{"personal": nil}); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), `"personal"`) || !strings.Contains(string(b), `"other"`) {
		t.Fatal("delete touched another server")
	}
}

func TestMCPAtRejectsInvalidPersonalConfig(t *testing.T) {
	c, path := userConfig(t, `{"mcp_servers":{}}`)
	if err := os.WriteFile(path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := MCPAt(path); err == nil {
		t.Fatal("accepted invalid config")
	}
	if err := c.PatchUserMCP(map[string]map[string]any{"bad.name": {"disabled": true}}); err == nil {
		t.Fatal("accepted invalid name")
	}
}
