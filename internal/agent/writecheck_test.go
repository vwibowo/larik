package agent

import (
	"path/filepath"
	"testing"

	"larik/internal/permission"
)

func TestDiscoveredWriteDestinationHonorsDeny(t *testing.T) {
	dir := t.TempDir()
	a := New(Options{Cwd: dir, Perms: permission.NewChecker(permission.ModeYolo, permission.Rules{Deny: []string{"edit(private/**)"}}, dir)})
	use := toolUse("action", "apply_code_action", `{"path":"public.go","line":1,"title":"rename"}`)
	if err := a.checkWriteDestination(use, filepath.Join(dir, "private", "secret.go")); err == nil {
		t.Fatal("destination deny bypassed")
	}
	if err := a.checkWriteDestination(use, filepath.Join(dir, "public.go")); err != nil {
		t.Fatal(err)
	}
}
