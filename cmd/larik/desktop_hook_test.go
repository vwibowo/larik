package main

import (
	"io"
	"strings"
	"testing"
)

func TestDesktopHookWithoutTabDoesNothing(t *testing.T) {
	t.Setenv("LARIK_DESKTOP_TAB", "")
	if err := runDesktopHook([]string{"claude", "PermissionRequest"}, strings.NewReader(`{"tool_input":{"secret":"private"}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopStatusRequiresTab(t *testing.T) {
	t.Setenv("LARIK_DESKTOP_TAB", "")
	err := runDesktopCommand("status", []string{"--id", "build", "--state", "working"}, strings.NewReader(""), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--tab") {
		t.Fatalf("missing tab: %v", err)
	}
}
