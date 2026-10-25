package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/permission"
)

func TestDiscoveredWriteDestinationHonorsDeny(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{Cwd: dir, Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{Deny: []string{"edit(private/**)"}}, dir)})
	use := toolUse("action", "apply_code_action", `{"path":"public.go","line":1,"title":"rename"}`)
	if err := a.checkWriteDestination(use, filepath.Join(dir, "private", "secret.go"), false); err == nil {
		t.Fatal("destination deny bypassed")
	}
	if err := a.checkWriteDestination(use, filepath.Join(dir, "public.go"), false); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoveredWriteDestinationNeedsMoreThanAScopedRule(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{Cwd: dir, Perms: permission.NewChecker(permission.ModeDefault, permission.Rules{Allow: []string{"edit(src/**)"}}, dir)})
	use := toolUse("action", "apply_code_action", `{"path":"src/a.go","line":1,"title":"rename"}`)
	if err := a.checkWriteDestination(use, filepath.Join(dir, "src", "b.go"), false); err != nil {
		t.Fatalf("destination the rule covers: %v", err)
	}
	err := a.checkWriteDestination(use, filepath.Join(dir, "lib", "b.go"), false)
	if err == nil || !strings.Contains(err.Error(), filepath.Join("lib", "b.go")) {
		t.Fatalf("destination outside the rule: %v", err)
	}
	// The user's approval covers every file the action changes.
	if err := a.checkWriteDestination(use, filepath.Join(dir, "lib", "b.go"), true); err != nil {
		t.Fatalf("user-approved action: %v", err)
	}
}
