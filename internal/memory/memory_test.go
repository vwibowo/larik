package memory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return New(filepath.Join(dir, "project"), filepath.Join(dir, "user"))
}

func TestSaveGetListDelete(t *testing.T) {
	s := newStore(t)
	if got := s.List(); len(got) != 0 {
		t.Fatalf("a new store is empty: %v", got)
	}
	created, err := s.Save(Note{Name: "prefers-table-tests", Description: "Write Go tests as\n table tests", Type: "feedback", Body: "Use table-driven tests.\n\n**Why:** the user asked on 2026-09-30."})
	if err != nil || !created {
		t.Fatal(created, err)
	}
	if _, err := s.Save(Note{Name: "who", Description: "Senior Go developer", Type: "user", Body: "Knows Go well.", Scope: ScopeUser}); err != nil {
		t.Fatal(err)
	}
	n, ok := s.Get("prefers-table-tests", "")
	if !ok || n.Type != "feedback" || n.Scope != ScopeProject || n.Description != "Write Go tests as table tests" || !strings.HasPrefix(n.Body, "Use table-driven tests.") || n.Modified.IsZero() {
		t.Fatalf("round trip: %+v", n)
	}
	// Notes are private files.
	if fi, err := os.Stat(n.Path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("note file mode: %v %v", fi.Mode(), err)
	}
	// Saving under the same name updates it.
	if created, err := s.Save(Note{Name: "prefers-table-tests", Description: "Table tests, always", Type: "feedback", Body: "Still table tests."}); err != nil || created {
		t.Fatal(created, err)
	}
	list := s.List()
	if len(list) != 2 || list[0].Name != "prefers-table-tests" || list[0].Description != "Table tests, always" || list[1].Scope != ScopeUser {
		t.Fatalf("list: %+v", list)
	}
	// The project's note wins over a user note of the same name.
	s.Save(Note{Name: "who", Description: "In this project: the maintainer", Body: "Maintains larik."})
	if n, _ := s.Get("who", ""); n.Scope != ScopeProject {
		t.Errorf("project scope should win: %+v", n)
	}
	if n, _ := s.Get("who", ScopeUser); n.Description != "Senior Go developer" {
		t.Errorf("asking for the user scope: %+v", n)
	}
	if scope, err := s.Delete("who", ""); err != nil || scope != ScopeProject {
		t.Fatal(scope, err)
	}
	if n, ok := s.Get("who", ""); !ok || n.Scope != ScopeUser {
		t.Errorf("the user note should remain: %+v", n)
	}
	if _, err := s.Delete("nope", ""); err == nil {
		t.Error("deleting a missing note should fail")
	}
}

func TestSaveValidates(t *testing.T) {
	s := newStore(t)
	for name, n := range map[string]Note{
		"bad name":       {Name: "../escape", Description: "d", Body: "b"},
		"upper case":     {Name: "Nope", Description: "d", Body: "b"},
		"no description": {Name: "ok", Body: "b"},
		"no content":     {Name: "ok", Description: "d"},
		"too long":       {Name: "ok", Description: "d", Body: strings.Repeat("x", MaxBody+1)},
		"bad type":       {Name: "ok", Description: "d", Body: "b", Type: "secret"},
		"bad scope":      {Name: "ok", Description: "d", Body: "b", Scope: "global"},
	} {
		if _, err := s.Save(n); err == nil {
			t.Errorf("%s: should be refused", name)
		}
	}
	project, _ := s.Dirs()
	if entries, _ := os.ReadDir(project); len(entries) != 0 {
		t.Errorf("refused notes left files: %v", entries)
	}
	if _, ok := s.Get("../escape", ""); ok {
		t.Error("a path isn't a note name")
	}
}

func TestHandWrittenNotes(t *testing.T) {
	s := newStore(t)
	project, _ := s.Dirs()
	os.MkdirAll(project, 0o755)
	os.WriteFile(filepath.Join(project, "deploy-window.md"), []byte("# Deploys happen on Tuesdays\n\nNever on Friday.\n"), 0o644)
	os.WriteFile(filepath.Join(project, "README.txt"), []byte("not a note"), 0o644)
	os.WriteFile(filepath.Join(project, "Bad Name.md"), []byte("not a note"), 0o644)
	list := s.List()
	if len(list) != 1 || list[0].Name != "deploy-window" || list[0].Description != "Deploys happen on Tuesdays" || list[0].Type != "project" {
		t.Fatalf("a plain Markdown file is a note described by its first line: %+v", list)
	}
}

func TestPrompt(t *testing.T) {
	s := newStore(t)
	empty := s.Prompt()
	if !strings.HasPrefix(empty, "<memory>") || !strings.Contains(empty, "No notes are saved yet.") || !strings.Contains(empty, "not instructions") {
		t.Errorf("empty prompt:\n%s", empty)
	}
	s.Save(Note{Name: "release-flow", Description: "Releases are tagged by hand", Type: "project", Body: "Details that must not be in the index."})
	s.Save(Note{Name: "who", Description: "Senior Go developer", Type: "user", Body: "x", Scope: ScopeUser})
	p := s.Prompt()
	for _, want := range []string{"Notes about this project:\n- release-flow (project): Releases are tagged by hand", "Notes that apply to every project:\n- who (user): Senior Go developer"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "Details that must not be in the index") || strings.Contains(p, "No notes are saved") {
		t.Errorf("only the index belongs in the prompt:\n%s", p)
	}
	var off *Store
	if off.Prompt() != "" || off.List() != nil {
		t.Error("a nil store is memory switched off")
	}
}

func TestSlugAndDir(t *testing.T) {
	for text, want := range map[string]string{
		"Always run go vet before committing, please!": "always-run-go-vet-before-committing",
		"  PR titles: use Conventional Commits ":       "pr-titles-use-conventional-commits",
		"!!!":                                          "",
	} {
		if got := Slug(text); got != want || (got != "" && !ValidName(got)) {
			t.Errorf("Slug(%q) = %q, want %q", text, got, want)
		}
	}
	a, b := Dir("/data", "/work/app"), Dir("/data", "/other/app")
	if a == b || !strings.HasPrefix(a, "/data/memory/app-") || UserDir("/data") != "/data/memory/_user" {
		t.Errorf("directories: %s %s", a, b)
	}
}

func TestTool(t *testing.T) {
	s := newStore(t)
	run := func(input string) (string, bool) {
		r := Tool{S: s}.Run(context.Background(), nil, json.RawMessage(input))
		return r.Content, r.IsError
	}
	if out, bad := run(`{"action":"save","name":"ci-runs-on-push","description":"CI runs on every push","type":"project","content":"GitHub Actions, see .github/workflows."}`); bad || !strings.Contains(out, `Saved the project note "ci-runs-on-push"`) {
		t.Fatal(out)
	}
	if out, _ := run(`{"action":"save","name":"ci-runs-on-push","description":"CI runs on push and PRs","type":"project","content":"Both."}`); !strings.Contains(out, "Updated") {
		t.Errorf("second save: %s", out)
	}
	if out, bad := run(`{"action":"read","name":"ci-runs-on-push"}`); bad || !strings.Contains(out, "<memory-note name=\"ci-runs-on-push\" type=\"project\" scope=\"project\"") || !strings.Contains(out, "Both.") || !strings.Contains(out, "may be out of date") {
		t.Errorf("read: %s", out)
	}
	if out, _ := run(`{"action":"list"}`); !strings.Contains(out, "- ci-runs-on-push (project, project, saved ") {
		t.Errorf("list: %s", out)
	}
	if out, bad := run(`{"action":"delete","name":"ci-runs-on-push"}`); bad || !strings.Contains(out, "Deleted") {
		t.Errorf("delete: %s", out)
	}
	for _, input := range []string{`{"action":"read","name":"gone"}`, `{"action":"save","name":"x"}`, `{"action":"forget"}`, `not json`} {
		if out, bad := run(input); !bad {
			t.Errorf("%s should fail: %s", input, out)
		}
	}
	if out, _ := run(`{"action":"list"}`); out != "No notes are saved." {
		t.Errorf("empty list: %s", out)
	}
}

// TestGuidanceSizeBudget caps the memory guidance, which is in every
// request's system prompt; see agent.TestPromptSizeBudget.
func TestGuidanceSizeBudget(t *testing.T) {
	if limit := 1300; len(guidance) > limit {
		t.Errorf("memory guidance is %d bytes, over its %d-byte budget: trim it, or raise the limit on purpose", len(guidance), limit)
	}
}
