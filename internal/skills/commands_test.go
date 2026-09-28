package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mkCmd(t *testing.T, path, content string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCommands(t *testing.T) {
	home, cfgDir, repo := t.TempDir(), t.TempDir(), t.TempDir()
	cmds := filepath.Join(repo, ".claude", "commands")
	mkCmd(t, filepath.Join(cmds, "fix-issue.md"), "---\ndescription: Fix a GitHub issue\nargument-hint: [issue-number] [priority]\n---\nFix issue #$1 following our style. Priority: $2.\n")
	mkCmd(t, filepath.Join(cmds, "frontend", "review_pr.md"), "# Review the open PR\n\nLook at $ARGUMENTS carefully.\n")
	mkCmd(t, filepath.Join(home, ".claude", "commands", "review_pr.md"), "user version\n")
	mkCmd(t, filepath.Join(cmds, "pdf.md"), "command pdf\n")
	mk(t, filepath.Join(repo, ".claude", "skills"), "pdf", "---\nname: pdf\ndescription: the pdf skill\n---\nskill pdf\n")
	mkCmd(t, filepath.Join(cmds, "bad name.md"), "x")

	set := Discover(Roots(home, cfgDir, repo, repo))
	fix, ok := set.Get("fix-issue")
	if !ok || !fix.Command || fix.Description != "Fix a GitHub issue" || fix.ArgumentHint != "[issue-number] [priority]" || !fix.ModelInvocable || !fix.UserInvocable {
		t.Fatalf("fix-issue: %+v %v", fix, ok)
	}
	pr, ok := set.Get("review_pr")
	if !ok || pr.Scope != "project" || pr.Description != "Review the open PR" || pr.ModelInvocable {
		t.Fatalf("review_pr (subdirectory, no frontmatter, project over user): %+v", pr)
	}
	if sk, _ := set.Get("pdf"); sk.Command {
		t.Error("a skill must win over a command of the same name")
	}
	if len(set.Warnings) != 1 || !strings.Contains(set.Warnings[0], "invalid command name") {
		t.Errorf("warnings: %v", set.Warnings)
	}

	out, ok := set.Expand("/fix-issue 123 high")
	if !ok || !strings.Contains(out, "invoked the /fix-issue command") || !strings.Contains(out, "Fix issue #123 following our style. Priority: high.") ||
		strings.Contains(out, "ARGUMENTS:") || !strings.Contains(out, "<command name=") {
		t.Fatalf("expand fix-issue: %s", out)
	}
	if out, _ := set.Expand("/review_pr the auth change"); !strings.Contains(out, "Look at the auth change carefully.") || !strings.Contains(out, "# Review the open PR") {
		t.Fatalf("expand review_pr: %s", out)
	}
	if idx := set.Index(); !strings.Contains(idx, "fix-issue") || strings.Contains(idx, "review_pr") {
		t.Errorf("only described commands go in the model's index:\n%s", idx)
	}
}

func TestSkillWithArgumentHint(t *testing.T) {
	root := t.TempDir()
	mk(t, root, "deploy", "---\nname: deploy\ndescription: Deploy\nargument-hint: [env] [version]\n---\nDeploy.\n")
	set := Discover([]Root{{Dir: root, Scope: "user"}})
	if sk, ok := set.Get("deploy"); !ok || len(set.Warnings) > 0 {
		t.Fatalf("a skill with an argument hint should load: %+v %v", sk, set.Warnings)
	}
}
