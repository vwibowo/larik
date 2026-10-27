package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// From NIM bench runs: the model wrote .../gn_T/... for the working
// directory's .../gn/T/... and listed directories for 30 turns.
func TestPathNoteNamesTheWorkingDirectory(t *testing.T) {
	cwd := filepath.Join(t.TempDir(), "gn", "T", "work")
	os.MkdirAll(filepath.Join(cwd, "calc"), 0o755)
	os.WriteFile(filepath.Join(cwd, "calc", "calc.go"), []byte("package calc"), 0o644)
	mangled := strings.Replace(cwd, "gn/T", "gn_T", 1)
	missing := "open " + mangled + "/calc/calc.go: no such file or directory"

	for name, input := range map[string]any{
		"read": map[string]string{"path": mangled + "/calc/calc.go"},
		"bash": map[string]string{"command": "ls -la " + mangled + "/calc/"},
		"dir":  map[string]string{"command": "cd " + mangled + " && ls"},
	} {
		in, _ := json.Marshal(input)
		note := pathNote(cwd, in, missing)
		if !strings.Contains(note, "did you mean "+cwd) {
			t.Errorf("%s: want a note pointing at %s, got %q", name, cwd, note)
		}
	}
	in, _ := json.Marshal(map[string]string{"path": "/nowhere/near/calc.go"})
	if note := pathNote(cwd, in, missing); note != "" {
		t.Errorf("a path far from the working directory got a note: %q", note)
	}
	in, _ = json.Marshal(map[string]string{"path": mangled + "/calc/calc.go"})
	if note := pathNote(cwd, in, "ok"); note != "" {
		t.Errorf("a result that isn't a missing path got a note: %q", note)
	}
	in, _ = json.Marshal(map[string]string{"path": cwd + "/calc/nope.go"})
	if note := pathNote(cwd, in, missing); note != "" {
		t.Errorf("a missing file in the real working directory got a note: %q", note)
	}
}
