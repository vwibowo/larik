package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinCommands(t *testing.T) {
	s := Discover(nil)
	for _, name := range []string{"review", "security-review"} {
		sk, ok := s.Get(name)
		if !ok || !sk.Builtin || !sk.Command || !sk.UserInvocable || sk.Scope != "built-in" {
			t.Fatalf("/%s should be a built-in command you can run: %+v", name, sk)
		}
		if sk.ModelInvocable {
			t.Errorf("/%s must not be listed for the model: it would change every session's system prompt", name)
		}
		if sk.Description == "" || sk.ArgumentHint == "" {
			t.Errorf("/%s needs a description and an argument hint for the palette: %+v", name, sk)
		}
		text, ok := s.Expand("/" + name + " main focus on error handling")
		if !ok {
			t.Fatalf("/%s didn't expand", name)
		}
		for _, want := range []string{ChangesMarker, `"main focus on error handling"`, "do not edit files", "<command name=\"" + name + "\">"} {
			if !strings.Contains(text, want) {
				t.Errorf("/%s lacks %q", name, want)
			}
		}
		if strings.Contains(text, "$ARGUMENTS") || strings.Contains(text, "base_dir") || strings.Contains(text, "description:") {
			t.Errorf("/%s wasn't rendered cleanly:\n%s", name, text[:300])
		}
	}
	// Shipping commands doesn't add a skills section to the system prompt.
	if idx := s.Index(); idx != "" {
		t.Errorf("index should be empty with only built-ins: %s", idx)
	}
}

func TestOwnCommandReplacesBuiltin(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "commands"), 0o755)
	os.WriteFile(filepath.Join(dir, "commands", "review.md"), []byte("Review it our way: $ARGUMENTS"), 0o644)
	s := Discover([]Root{{Dir: filepath.Join(dir, "commands"), Scope: "project", Commands: true}})
	sk, _ := s.Get("review")
	if sk.Builtin || sk.Scope != "project" {
		t.Fatalf("the project's review.md should win: %+v", sk)
	}
	if text, _ := s.Expand("/review now"); !strings.Contains(text, "Review it our way: now") || strings.Contains(text, ChangesMarker) {
		t.Errorf("expanded the wrong command:\n%s", text)
	}
	if len(s.Shadowed) != 0 {
		t.Errorf("replacing a built-in isn't a conflict to report: %+v", s.Shadowed)
	}
	if sk, _ := s.Get("security-review"); !sk.Builtin {
		t.Error("the other built-in should still be there")
	}
	// Reload keeps the built-ins.
	os.Remove(filepath.Join(dir, "commands", "review.md"))
	s.Reload()
	if sk, _ := s.Get("review"); !sk.Builtin {
		t.Error("the built-in should come back when the override is removed")
	}
}
