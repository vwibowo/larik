package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mk(t *testing.T, root, dir, content string) string {
	t.Helper()
	p := filepath.Join(root, dir, "SKILL.md")
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiscoverPrecedenceAndParsing(t *testing.T) {
	home, cfgDir, repo := t.TempDir(), t.TempDir(), t.TempDir()
	cwd := filepath.Join(repo, "sub")
	os.MkdirAll(cwd, 0o755)

	mk(t, filepath.Join(home, ".claude", "skills"), "pdf", "---\nname: pdf\ndescription: user pdf skill\n---\nuser body\n")
	mk(t, filepath.Join(repo, ".larik", "skills"), "pdf", "---\nname: pdf\ndescription: |\n  project pdf\n  skill\n---\n# PDF\nproject body\n")
	mk(t, filepath.Join(cwd, ".claude", "skills"), "deploy", "---\nname: deploy\ndescription: Deploy the app\ndisable-model-invocation: true\n---\nDeploy to $ARGUMENTS now.\n")
	mk(t, filepath.Join(cfgDir, "skills"), "helper", "---\ndescription: \"quoted: with colon\"\nuser-invocable: false\n---\nhelp\n")
	mk(t, filepath.Join(cfgDir, "skills"), "broken", "no frontmatter here")
	mk(t, filepath.Join(cfgDir, "skills"), "Bad_Name", "---\nname: Bad_Name\ndescription: x\n---\n")
	// A symlinked duplicate of the same skill must not count as shadowed.
	os.MkdirAll(filepath.Join(home, ".agents", "skills"), 0o755)
	os.Symlink(filepath.Join(cfgDir, "skills", "helper"), filepath.Join(home, ".agents", "skills", "helper"))

	set := Discover(Roots(home, cfgDir, cwd, repo))

	pdf, _ := set.Get("pdf")
	if pdf.Scope != "project" || pdf.Description != "project pdf skill" {
		t.Errorf("project skill should win: %+v", pdf)
	}
	if len(set.Shadowed) != 1 || set.Shadowed[0].Scope != "user" {
		t.Errorf("shadowed = %+v", set.Shadowed)
	}
	helper, ok := set.Get("helper")
	if !ok || helper.Description != "quoted: with colon" || helper.UserInvocable {
		t.Errorf("helper = %+v", helper)
	}
	if len(set.Warnings) != 2 {
		t.Errorf("want 2 warnings (missing frontmatter, bad name), got %v", set.Warnings)
	}

	idx := set.Index()
	if !strings.Contains(idx, "- pdf: project pdf skill") || !strings.Contains(idx, "- helper:") || strings.Contains(idx, "deploy") {
		t.Errorf("index:\n%s", idx)
	}
	if _, body, _ := set.Body("pdf"); body != "# PDF\nproject body" {
		t.Errorf("body = %q", body)
	}
}

func TestExpand(t *testing.T) {
	root := t.TempDir()
	mk(t, root, "deploy", "---\nname: deploy\ndescription: d\n---\nDeploy to $ARGUMENTS now.\n")
	mk(t, root, "review", "---\nname: review\ndescription: r\n---\nReview the diff.\n")
	mk(t, root, "hidden", "---\nname: hidden\ndescription: h\nuser-invocable: false\n---\nx\n")
	set := Discover([]Root{{Dir: root, Scope: "project"}})

	got, ok := set.Expand("/deploy staging")
	if !ok || !strings.Contains(got, "Deploy to staging now.") || strings.Contains(got, "ARGUMENTS:") {
		t.Errorf("deploy: %q", got)
	}
	got, _ = set.Expand("/review focus on auth")
	if !strings.Contains(got, "Review the diff.") || !strings.Contains(got, "ARGUMENTS: focus on auth") || !strings.Contains(got, `base_dir="`+filepath.Join(root, "review")) {
		t.Errorf("review: %q", got)
	}
	for _, p := range []string{"/hidden", "/nope", "deploy", "/"} {
		if _, ok := set.Expand(p); ok {
			t.Errorf("%q should not expand", p)
		}
	}
	var nilSet *Set
	if _, ok := nilSet.Expand("/deploy"); ok {
		t.Error("nil set")
	}
}

func TestTool(t *testing.T) {
	root := t.TempDir()
	mk(t, root, "pdf", "---\nname: pdf\ndescription: d\n---\nUse scripts/fill.py.\n")
	tool := Tool{Set: Discover([]Root{{Dir: root, Scope: "user"}})}
	r := tool.Run(context.Background(), nil, json.RawMessage(`{"name":"pdf"}`))
	if r.IsError || !strings.Contains(r.Content, "Use scripts/fill.py.") || !strings.Contains(r.Content, filepath.Join(root, "pdf")) {
		t.Errorf("%+v", r)
	}
	if r := tool.Run(context.Background(), nil, json.RawMessage(`{"name":"missing"}`)); !r.IsError {
		t.Error("missing skill should error")
	}
	mk(t, root, "deploy", "---\nname: deploy\ndescription: d\ndisable-model-invocation: true\n---\nShip it.\n")
	tool = Tool{Set: Discover([]Root{{Dir: root, Scope: "user"}})}
	if r := tool.Run(context.Background(), nil, json.RawMessage(`{"name":"deploy"}`)); !r.IsError || strings.Contains(r.Content, "Ship it") {
		t.Errorf("a disable-model-invocation skill must not load through the tool: %+v", r)
	}
}

func TestReload(t *testing.T) {
	root := t.TempDir()
	set := Discover([]Root{{Dir: root, Scope: "project"}})
	if set.Index() != "" {
		t.Fatal("no skills yet")
	}
	mk(t, root, "notes", "---\nname: notes\ndescription: Take notes\n---\nbody\n")
	set.Reload()
	if _, ok := set.Get("notes"); !ok || !strings.Contains(set.Index(), "notes: Take notes") {
		t.Fatalf("reload should find the new skill: %q", set.Index())
	}
}

// TestHide: hidden skills leave the index and the skill tool, stay
// available as /name, and stay hidden after a reload.
func TestHide(t *testing.T) {
	root := t.TempDir()
	mk(t, root, "cmux-browser", "---\nname: cmux-browser\ndescription: Drive a browser\n---\nBrowse.\n")
	mk(t, root, "pdf", "---\nname: pdf\ndescription: Fill PDFs\n---\nFill.\n")
	set := Discover([]Root{{Dir: root, Scope: "user"}})
	set.Hide([]string{"cmux-*"})
	check := func(when string) {
		t.Helper()
		if idx := set.Index(); strings.Contains(idx, "cmux-browser") || !strings.Contains(idx, "- pdf: Fill PDFs") {
			t.Errorf("%s: index:\n%s", when, idx)
		}
		if r := (Tool{Set: set}).Run(context.Background(), nil, json.RawMessage(`{"name":"cmux-browser"}`)); !r.IsError {
			t.Errorf("%s: a hidden skill must not load through the tool: %+v", when, r)
		}
		if got, ok := set.Expand("/cmux-browser"); !ok || !strings.Contains(got, "Browse.") {
			t.Errorf("%s: the user may still run a hidden skill: %q", when, got)
		}
	}
	check("after Hide")
	set.Reload()
	check("after Reload")

	if sk, _ := set.Get("cmux-browser"); !sk.Hidden {
		t.Error("a hidden skill should say so, for /skills")
	}

	mk(t, root, "deploy", "---\nname: deploy\ndescription: Deploy\ndisable-model-invocation: true\n---\nShip.\n")
	set.Reload()
	set.Hide([]string{"*"})
	if set.Index() != "" {
		t.Errorf("with every skill hidden there is no index: %q", set.Index())
	}
	if sk, _ := set.Get("deploy"); sk.Hidden {
		t.Error("a manual-only skill isn't hidden by the setting; its frontmatter keeps it manual")
	}
	set.Hide(nil)
	if idx := set.Index(); !strings.Contains(idx, "cmux-browser") || strings.Contains(idx, "deploy") {
		t.Errorf("unhiding should offer the hidden skills again, and only those:\n%s", idx)
	}
}

// TestIndexBudget: long descriptions are clipped and, past the budget,
// skills are still named so the model can load them.
func TestIndexBudget(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("word ", 200)
	for i := range 60 {
		name := fmt.Sprintf("skill-%02d", i)
		mk(t, root, name, "---\nname: "+name+"\ndescription: "+long+"\n---\nbody\n")
	}
	idx := Discover([]Root{{Dir: root, Scope: "project"}}).Index()
	if len(idx) > indexBudget+2000 {
		t.Fatalf("index is %d bytes", len(idx))
	}
	for i := range 60 {
		if name := fmt.Sprintf("skill-%02d", i); !strings.Contains(idx, name) {
			t.Errorf("%s is missing from the index", name)
		}
	}
	if !strings.Contains(idx, "More skills") || strings.Contains(idx, long[:maxIndexDesc+10]) {
		t.Errorf("descriptions should be clipped and overflow named:\n%s", idx)
	}
}
