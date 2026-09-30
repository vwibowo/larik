package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"larik/internal/memory"
)

func TestMemoryCommand(t *testing.T) {
	m := testModel(t)
	run := func(line string) string {
		t.Helper()
		var got string
		info := func(s string) tea.Cmd { got = s; return nil }
		m.memoryCommand(strings.Fields(line), info, info)
		return ansi.Strip(got)
	}

	if got := run(""); !strings.Contains(got, "memory is off") {
		t.Fatalf("without a store: %s", got)
	}
	dir := t.TempDir()
	m.opts.Memory = memory.New(filepath.Join(dir, "p"), filepath.Join(dir, "u"))
	if got := run(""); !strings.Contains(got, "notes for this project") || strings.Count(got, "(none)") != 2 {
		t.Errorf("empty list: %s", got)
	}

	if got := run("add Always run go vet before committing"); got != "remembered as always-run-go-vet-before-committing (project scope)" {
		t.Errorf("add: %s", got)
	}
	// The same opening words don't overwrite the first note.
	if got := run("add Always run go vet before committing and pushing"); !strings.Contains(got, "always-run-go-vet-before-committing-2") {
		t.Errorf("second add: %s", got)
	}
	if got := run("add --user Prefers short answers"); got != "remembered as prefers-short-answers (user scope)" {
		t.Errorf("add --user: %s", got)
	}
	list := run("")
	for _, want := range []string{"always-run-go-vet-before-committing ", "always-run-go-vet-before-committing-2", "prefers-short-answers"} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}
	if got := run("show prefers-short-answers"); !strings.Contains(got, "Prefers short answers") || !strings.Contains(got, "user") {
		t.Errorf("show: %s", got)
	}
	if got := run("delete prefers-short-answers"); got != "deleted the user note prefers-short-answers" {
		t.Errorf("delete: %s", got)
	}
	if _, ok := m.opts.Memory.Get("prefers-short-answers", ""); ok {
		t.Error("the note is still there")
	}
	for _, bad := range []string{"show", "show nope", "delete nope", "add", "add !!!", "frobnicate"} {
		if got := run(bad); got == "" || strings.HasPrefix(got, "remembered") || strings.HasPrefix(got, "deleted") {
			t.Errorf("%q should be refused: %q", bad, got)
		}
	}
}
