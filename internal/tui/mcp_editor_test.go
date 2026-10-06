package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"larik/internal/config"
)

// An environment or header credential goes with its key when cleared, and d
// means nothing on a row that is not a credential, so it cannot wipe a field
// you can see and would have edited with enter.
func TestMCPEditorClearsCredentialsOnlyOnPurpose(t *testing.T) {
	m := testModel(t)
	path := m.opts.Config.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := `{"mcp_servers":{"mine":{"command":"srv","env":{"TOKEN":"env-secret"}}}}`
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	m.command("/mcp-config")
	e := m.settings.mcp
	e.name = "mine"
	e.rebuild()

	e.list.selectWhere(func(it pickItem) bool { return it.value == "command" })
	m.Update(typedKey('d'))
	if len(e.changes) != 0 {
		t.Fatalf("d touched a field that is not a credential: %v", e.changes)
	}

	e.list.selectWhere(func(it pickItem) bool { return it.value == "env:TOKEN" })
	m.Update(typedKey('d'))
	if _, ok := e.servers["mine"].Env["TOKEN"]; ok {
		t.Fatal("d should remove the environment entry with its key")
	}
	m.saveMCPEditor()
	saved := savedConfig(t, m)
	if strings.Contains(saved, "TOKEN") || strings.Contains(saved, "env-secret") {
		t.Errorf("cleared credential survived: %s", saved)
	}
	if !strings.Contains(saved, `"command": "srv"`) {
		t.Errorf("clearing a credential lost the rest of the server: %s", saved)
	}
}

func TestMCPEditorPersonalOnlyMaskedPatch(t *testing.T) {
	m := testModel(t)
	path := m.opts.Config.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"mcp_servers":{"mine":{"type":"http","url":"https://example.org","headers":{"Authorization":"top-secret"},"oauth":{"client_secret":"oauth-secret"},"future":42}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	m.opts.Config.MCPServers = map[string]config.MCPServer{"project": {Command: "danger", Env: map[string]string{"TOKEN": "project-secret"}}}
	m.command("/config mcp_servers")
	e := m.settings.mcp
	if e == nil || len(e.servers) != 1 {
		t.Fatal("merged project servers entered editor")
	}
	e.name = "mine"
	e.rebuild()
	for _, secret := range []string{"top-secret", "oauth-secret", "project-secret", "danger"} {
		if strings.Contains(m.mcpEditorView(), secret) {
			t.Fatalf("leaked %s", secret)
		}
	}
	e.startInput("headers:Authorization", m.width)
	if e.input.Value() != "" {
		t.Fatal("secret prefilled")
	}
	e.input.SetValue("new-secret")
	e.finishInput()
	// Leaving a masked field empty backs out of replacing it; clearing the
	// secret is d, so an empty input can never erase one you cannot see.
	e.startInput("oauth:client_secret", m.width)
	e.finishInput()
	if !strings.Contains(e.status, "d clears") {
		t.Fatalf("an empty input should say how to clear instead: %q", e.status)
	}
	e.list.selectWhere(func(it pickItem) bool { return it.value == "oauth:client_secret" })
	m.Update(typedKey('d'))
	e.change("disabled", true)
	m.saveMCPEditor()
	saved := savedConfig(t, m)
	for _, want := range []string{`"Authorization": "new-secret"`, `"future": 42`, `"disabled": true`} {
		if !strings.Contains(saved, want) {
			t.Errorf("lost %s: %s", want, saved)
		}
	}
	for _, unwanted := range []string{"oauth-secret", "project-secret", "danger"} {
		if strings.Contains(saved, unwanted) {
			t.Errorf("persisted %s", unwanted)
		}
	}
}

func TestMCPEditorCRUDAndTransport(t *testing.T) {
	m := testModel(t)
	m.command("/mcp-config")
	e := m.settings.mcp
	e.startInput("name", m.width)
	e.input.SetValue("remote")
	e.finishInput()
	e.startInput("type", m.width)
	e.input.SetValue("sse")
	e.finishInput()
	e.startInput("url", m.width)
	e.input.SetValue("not-a-url")
	e.finishInput()
	if !e.failed {
		t.Fatal("invalid URL accepted")
	}
	e.input.SetValue("https://example.org/sse")
	e.finishInput()
	e.startInput("oauth:scopes", m.width)
	e.input.SetValue("read, write")
	e.finishInput()
	e.startInput("oauth:callback_port", m.width)
	e.input.SetValue("8077")
	e.finishInput()
	e.startInput("env+", m.width)
	e.input.SetValue("TOKEN")
	e.finishInput()
	e.input.SetValue("secret")
	e.finishInput()
	m.saveMCPEditor()
	saved := savedConfig(t, m)
	for _, want := range []string{`"type": "sse"`, `"url": "https://example.org/sse"`, `"read"`, `"write"`, `"callback_port": 8077`, `"TOKEN": "secret"`} {
		if !strings.Contains(saved, want) {
			t.Errorf("missing %s: %s", want, saved)
		}
	}
	m.command("/mcp-config")
	e = m.settings.mcp
	e.name = "remote"
	e.rebuild()
	e.startInput("type", m.width)
	e.input.SetValue("stdio")
	e.finishInput()
	e.startInput("command", m.width)
	e.input.SetValue("npx")
	e.finishInput()
	e.startInput("args", m.width)
	e.input.SetValue("-y, server")
	e.finishInput()
	m.saveMCPEditor()
	saved = savedConfig(t, m)
	if !strings.Contains(saved, `"command": "npx"`) || !strings.Contains(saved, `"-y"`) || strings.Contains(saved, `"TOKEN"`) || strings.Contains(saved, `"callback_port"`) || strings.Contains(saved, `"url"`) {
		t.Fatalf("transport switch did not clear old fields: %s", saved)
	}
	m.command("/mcp-config")
	e = m.settings.mcp
	e.name = "remote"
	e.rebuild()
	e.list.selectWhere(func(it pickItem) bool { return it.value == "delete" })
	m.Update(press(tea.KeyEnter))
	m.saveMCPEditor()
	if strings.Contains(savedConfig(t, m), `"remote"`) {
		t.Fatal("server not deleted")
	}
}
