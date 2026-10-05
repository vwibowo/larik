package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"larik/internal/hooks"

	"larik/internal/config"
)

func TestHooksEditorCommandAndProjectIsolation(t *testing.T) {
	m := testModel(t)
	m.opts.Config.ProjectHooks = hooks.Config{hooks.Stop: {{Hooks: []hooks.Command{{Type: "command", Command: "project-only"}}}}}
	m.command("/config hooks")
	if m.settings.hooks == nil || strings.Contains(m.hooksEditorView(), "project-only") {
		t.Fatal("editor showed project hooks")
	}
	m.command("/hooks-config")
	e := m.settings.hooks
	e.list.selectWhere(func(it pickItem) bool { return it.value == "Stop" })
	m.Update(press(tea.KeyEnter))
	e.list.selectWhere(func(it pickItem) bool { return it.value == "add" })
	m.Update(press(tea.KeyEnter))
	e.list.selectWhere(func(it pickItem) bool { return it.value == "command" })
	m.Update(press(tea.KeyEnter))
	e.startInput("command", m.width)
	e.input.SetValue("echo personal")
	e.finishInput()
	m.saveHooksEditor()
	data := savedConfig(t, m)
	if !strings.Contains(data, "echo personal") || strings.Contains(data, "project-only") {
		t.Fatalf("personal save: %s", data)
	}
}

func TestHooksEditorHierarchyAndValidation(t *testing.T) {
	c := &config.Config{ConfigDir: t.TempDir()}
	e := newHooksEditor(c)
	e.event = "PreToolUse"
	e.rebuild()
	e.events[e.event] = append(e.matchers(), map[string]any{"matcher": "[", "hooks": []any{map[string]any{"type": "command", "command": "echo safe"}}})
	e.matcher = 0
	e.dirty()
	if err := e.validate(); err == nil {
		t.Fatal("invalid regex accepted")
	}
	e.matcherMap()["matcher"] = "bash|edit"
	if err := e.validate(); err != nil {
		t.Fatal(err)
	}
	e.hook = 0
	e.hookMap()["timeout"] = -1
	if err := e.validate(); err == nil {
		t.Fatal("negative timeout accepted")
	}
	e.hookMap()["timeout"] = 60
	e.hookMap()["type"] = "prompt"
	delete(e.hookMap(), "command")
	if err := e.validate(); err == nil {
		t.Fatal("empty prompt accepted")
	}
	e.hookMap()["prompt"] = "May this proceed? $ARGUMENTS"
	e.hookMap()["model"] = "not-a-role"
	if err := e.validate(); err == nil {
		t.Fatal("invalid model accepted")
	}
	e.hookMap()["model"] = "explore"
	if err := e.validate(); err != nil {
		t.Fatal(err)
	}
	if err := c.PatchUserHooks(e.changed); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.PersonalHooksAt(c.UserConfigPath())
	if err != nil || len(loaded["PreToolUse"]) != 1 {
		t.Fatalf("reload: %v %v", loaded, err)
	}
}

func TestHooksEditorPersonalReadFailure(t *testing.T) {
	c := &config.Config{ConfigDir: t.TempDir()}
	path := c.UserConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"hooks":{"Stop":42}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if newHooksEditor(c).readFailed == nil {
		t.Fatal("malformed hooks silently discarded")
	}
}
