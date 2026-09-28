package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/session"
)

func TestFindMentions(t *testing.T) {
	cases := map[string][]mention{
		"look at @main.go please":         {{path: "main.go"}},
		"@a.go, @b.go.":                   {{path: "a.go"}, {path: "b.go"}},
		"mail me@example.com":             nil,
		`see @"my notes.md" now`:          {{path: "my notes.md"}},
		"@src/x.go#L10-40 and @y.go#L3":   {{path: "src/x.go", from: 10, to: 40}, {path: "y.go", from: 3, to: 3}},
		"@bad#L9-2":                       {{path: "bad#L9-2"}},
		"(@dir/)":                         nil,
		"first line\n@next.go":            {{path: "next.go"}},
		"just @":                          nil,
		`@"unterminated quote @after.txt`: {{path: "after.txt"}},
	}
	for in, want := range cases {
		if got := findMentions(in); !reflect.DeepEqual(got, want) {
			t.Errorf("findMentions(%q) = %+v, want %+v", in, got, want)
		}
	}
}

// lastPrompt is the user message of the first request.
func lastPrompt(t *testing.T, fp *fakeProvider) llm.Message {
	t.Helper()
	msgs := fp.requests[0].Messages
	return msgs[len(msgs)-1]
}

func notices(evs []Event) string {
	var out []string
	for _, e := range evs {
		if e.Kind == EvNotice {
			out = append(out, e.Text)
		}
	}
	return strings.Join(out, "\n")
}

func TestMentionsAttachFilesImagesAndDirs(t *testing.T) {
	a, fp, dir := setup(t, permission.ModeDefault, assistant(llm.TextBlock("ok")))
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "shot.png"), []byte("\x89PNG fake"), 0o644)
	os.WriteFile(filepath.Join(dir, "blob.bin"), []byte("a\x00b"), 0o644)
	os.Mkdir(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "pkg", "x.go"), nil, 0o644)

	prompt := "explain @main.go#L3 and @shot.png, @pkg @blob.bin @alice @main.go"
	evs := drain(a.Run(context.Background(), prompt), PermissionReply{})
	msg := lastPrompt(t, fp)
	if got := msg.Text(); got != prompt {
		t.Fatalf("Text() should be the prompt as typed, got %q", got)
	}
	if !session.IsPrompt(msg) {
		t.Fatal("a prompt with attachments is still a prompt")
	}
	var file, image, listing string
	for _, b := range msg.Blocks[1:] {
		if b.Attachment == "" {
			t.Fatalf("unmarked block after the prompt: %+v", b)
		}
		switch {
		case b.Type == llm.BlockImage:
			image = b.MediaType
		case strings.HasPrefix(b.Text, "<file"):
			file = b.Text
		case strings.HasPrefix(b.Text, "<directory"):
			listing = b.Text
		}
	}
	if !strings.Contains(file, `lines="3-3"`) || !strings.Contains(file, "func main() {}") || strings.Contains(file, "package main") {
		t.Errorf("file range: %q", file)
	}
	if image != "image/png" {
		t.Errorf("image media type %q", image)
	}
	if !strings.Contains(listing, "x.go") {
		t.Errorf("listing: %q", listing)
	}
	n := notices(evs)
	for _, want := range []string{"attached @main.go (1 line)", "attached @shot.png (image", "attached @pkg (1 entries)", "couldn't attach @blob.bin"} {
		if !strings.Contains(n, want) {
			t.Errorf("notices lack %q:\n%s", want, n)
		}
	}
	if strings.Count(n, "@main.go") != 1 || strings.Contains(n, "alice") {
		t.Errorf("duplicates and missing paths should be skipped quietly:\n%s", n)
	}
}

func TestMentionedFileCanBeEditedWithoutReading(t *testing.T) {
	a, _, dir := setup(t, permission.ModeYolo,
		assistant(toolUse("t1", "edit", `{"path":"notes.txt","old_string":"old","new_string":"new"}`)),
		assistant(llm.TextBlock("done")),
	)
	path := filepath.Join(dir, "notes.txt")
	os.WriteFile(path, []byte("old\n"), 0o644)
	for _, e := range drain(a.Run(context.Background(), "fix @notes.txt"), PermissionReply{}) {
		if e.Kind == EvToolEnd && e.IsError {
			t.Fatalf("edit after mention failed: %s", e.Output)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != "new\n" {
		t.Fatalf("content %q", data)
	}
}

func TestMentionDenyRuleWins(t *testing.T) {
	a, fp, _ := setup(t, permission.ModeDefault, assistant(llm.TextBlock("ok")))
	a.opts.Perms = permission.NewChecker(permission.ModeDefault, permission.Rules{Deny: []string{"read(**/.env)"}}, a.opts.Cwd)
	os.WriteFile(filepath.Join(a.opts.Cwd, ".env"), []byte("KEY=secret\n"), 0o644)
	evs := drain(a.Run(context.Background(), "use @.env"), PermissionReply{})
	if len(lastPrompt(t, fp).Blocks) != 1 || !strings.Contains(notices(evs), "not attaching @.env") {
		t.Fatalf("denied file was attached: %+v\n%s", lastPrompt(t, fp).Blocks, notices(evs))
	}
}

func TestShellOutputRidesOnNextPrompt(t *testing.T) {
	a, fp, _ := setup(t, permission.ModePlan, assistant(llm.TextBlock("ok")))
	res := a.Shell(context.Background(), "echo hello-from-shell")
	if res.IsError || !strings.Contains(res.Content, "hello-from-shell") {
		t.Fatalf("shell: %+v", res)
	}
	drain(a.Run(context.Background(), "what did it print?"), PermissionReply{})
	msg := lastPrompt(t, fp)
	// Plan mode adds its note in front of the prompt.
	if !strings.HasSuffix(msg.Text(), "what did it print?") || len(msg.Blocks) != 2 || msg.Blocks[1].Attachment != "!echo hello-from-shell" ||
		!strings.Contains(msg.Blocks[1].Text, "<bash-output>hello-from-shell</bash-output>") {
		t.Fatalf("prompt blocks: %+v", msg.Blocks)
	}

	a.opts.Perms = permission.NewChecker(permission.ModeDefault, permission.Rules{Deny: []string{"bash(rm*)"}}, a.opts.Cwd)
	if res := a.Shell(context.Background(), "rm -rf nothing"); !res.IsError {
		t.Fatal("deny rules should stop shell commands")
	}
}

func TestSystemPromptsDontExpandMentions(t *testing.T) {
	a, fp, dir := setup(t, permission.ModeDefault, assistant(llm.TextBlock("ok")))
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	ch := make(chan Event, 64)
	go func() {
		defer close(ch)
		a.runWith(context.Background(), "task finished, see @main.go", true, func(e Event) { ch <- e })
	}()
	drain(ch, PermissionReply{})
	if n := len(lastPrompt(t, fp).Blocks); n != 1 {
		t.Fatalf("system prompt got %d blocks", n)
	}
}
