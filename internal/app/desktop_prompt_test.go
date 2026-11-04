package app

import (
	"strings"
	"testing"
)

func TestDesktopTabGetsControlGuidance(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cwd := t.TempDir()
	hasTool := func(a *App, name string) bool {
		for _, tool := range a.tools {
			if tool.Spec().Name == name {
				return true
			}
		}
		return false
	}

	t.Setenv("LARIK_DESKTOP_TAB", "")
	outside, err := Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(outside.SystemPrompt(), "<larik_desktop>") || hasTool(outside, "desktop_page_navigate") || hasTool(outside, "desktop_terminal_open") {
		t.Fatal("Desktop guidance or tools appeared outside a Desktop tab")
	}
	outside.Close()

	t.Setenv("LARIK_DESKTOP_TAB", "tab-123")
	inside, err := Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer inside.Close()
	prompt := inside.SystemPrompt()
	for _, want := range []string{"<larik_desktop>", "desktop_page_navigate", "desktop_terminal_open", "desktop_terminal_send", `"sandbox" to false`, "$LARIK_DESKTOP_TAB"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("Desktop prompt missing %q", want)
		}
	}
	if !hasTool(inside, "desktop_page_navigate") || !hasTool(inside, "desktop_terminal_open") {
		t.Fatal("Desktop tools missing inside a Desktop tab")
	}
	t.Setenv("LARIK_DESKTOP_TAB", "")
	if inside.SystemPrompt() != prompt {
		t.Fatal("Desktop prompt changed after setup")
	}
}
