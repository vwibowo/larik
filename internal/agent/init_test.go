package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
)

func TestInitExpands(t *testing.T) {
	a, fp, dir := setup(t, permission.ModeDefault, assistant(llm.TextBlock("wrote it")), assistant(llm.TextBlock("improved it")))
	drain(a.Run(context.Background(), "/init focus on the tests"), PermissionReply{})
	got := fp.requests[0].Messages[0].Text()
	if !strings.Contains(got, "Create "+filepath.Join(dir, "AGENTS.md")) || !strings.Contains(got, "The user added: focus on the tests") {
		t.Fatalf("prompt = %q", got)
	}

	os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# notes\n"), 0o644)
	drain(a.Run(context.Background(), "/init"), PermissionReply{})
	last := fp.requests[1].Messages
	if got := last[len(last)-1].Text(); !strings.Contains(got, "Improve "+filepath.Join(dir, "CLAUDE.md")) {
		t.Fatalf("an existing CLAUDE.md should be improved, not shadowed: %q", got)
	}
}

func TestInitNeedsWordBoundary(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeDefault, assistant(llm.TextBlock("ok")))
	drain(a.Run(context.Background(), "/initialize the db"), PermissionReply{})
	if got := fp.requests[0].Messages[0].Text(); got != "/initialize the db" {
		t.Fatalf("prompt = %q", got)
	}
}
