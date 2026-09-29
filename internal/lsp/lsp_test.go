package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"larik/internal/tools"
)

func buildFake(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fakels")
	if out, err := exec.Command("go", "build", "-o", bin, "./testdata/fakels").CombinedOutput(); err != nil {
		t.Fatalf("build fakels: %v\n%s", err, out)
	}
	return bin
}

func fakeManager(t *testing.T) (*Manager, string) {
	t.Helper()
	bin := buildFake(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	m := NewManager(map[string]ServerConfig{
		"fake":  {Command: []string{bin}, Extensions: []string{".fake"}},
		"gopls": {Disabled: true}, // keep the test hermetic
	}, dir, dir, filepath.Join(dir, "logs"))
	t.Cleanup(m.Close)
	return m, dir
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiagnosticsAfterEdit(t *testing.T) {
	m, dir := fakeManager(t)
	path := filepath.Join(dir, "a.fake")
	ctx := context.Background()

	write(t, path, "def main\nok\n")
	if d := m.Diagnostics(ctx, path); d != "" {
		t.Fatalf("clean file should report nothing, got %q", d)
	}

	write(t, path, "def main\nthis is BAD\nMEH here\n")
	d := m.Diagnostics(ctx, path)
	for _, want := range []string{`<diagnostics file="a.fake">`, "ERROR 2:9 found BAD (fakels)", "WARN 3:1 meh"} {
		if !strings.Contains(d, want) {
			t.Errorf("missing %q in:\n%s", want, d)
		}
	}
	if strings.Index(d, "ERROR") > strings.Index(d, "WARN") {
		t.Error("errors should be listed before warnings")
	}

	write(t, path, "def main\nBREAKOTHER\n")
	if d := m.Diagnostics(ctx, path); !strings.Contains(d, "introduced errors in other files: other.fake (1)") {
		t.Errorf("cross-file report missing:\n%s", d)
	}

	// Files of other languages, or outside the project, are ignored.
	if d := m.Diagnostics(ctx, filepath.Join(dir, "x.txt")); d != "" {
		t.Error("no server for .txt")
	}
	var fake *Status
	for _, st := range m.Statuses() {
		if st.Name == "fake" {
			fake = &st
		}
	}
	if fake == nil || len(fake.Running) != 1 || fake.Running[0] != "." {
		t.Errorf("fake status = %+v", fake)
	}
}

func TestEditToolIntegration(t *testing.T) {
	m, dir := fakeManager(t)
	env := tools.NewEnv(dir)
	env.Diagnostics, env.Touch = m.Diagnostics, m.Touch
	write(t, filepath.Join(dir, "b.fake"), "def f\nfine\n")

	tools.Read{}.Run(context.Background(), env, json.RawMessage(`{"path":"b.fake"}`))
	res := tools.Edit{}.Run(context.Background(), env, json.RawMessage(`{"path":"b.fake","old_string":"fine","new_string":"BAD"}`))
	if res.IsError || !strings.Contains(res.Content, "Edited") || !strings.Contains(res.Content, "ERROR 2:1 found BAD") {
		t.Fatalf("edit result:\n%s", res.Content)
	}
}

func TestNavigationTool(t *testing.T) {
	m, dir := fakeManager(t)
	write(t, filepath.Join(dir, "lib.fake"), "x\ndef helper\n")
	write(t, filepath.Join(dir, "main.fake"), "def main\n  call helper\n")
	env := tools.NewEnv(dir)
	tl := Tool{M: m}
	run := func(in string) tools.Result { return tl.Run(context.Background(), env, json.RawMessage(in)) }

	// Open lib.fake so the fake server knows it.
	m.Diagnostics(context.Background(), filepath.Join(dir, "lib.fake"))

	if r := run(`{"operation":"definition","path":"main.fake","line":2,"column":9}`); r.IsError || !strings.HasPrefix(r.Content, "lib.fake:2:5  def helper") {
		t.Errorf("definition: %+v", r)
	}
	if r := run(`{"operation":"references","path":"main.fake","line":2,"column":9}`); !strings.Contains(r.Content, "lib.fake:2:5") || !strings.Contains(r.Content, "main.fake:2:8") {
		t.Errorf("references: %+v", r)
	}
	if r := run(`{"operation":"hover","path":"main.fake","line":2,"column":9}`); r.Content != "hover: helper" {
		t.Errorf("hover: %+v", r)
	}
	if r := run(`{"operation":"symbols","path":"main.fake"}`); r.Content != "function main  :1\n" {
		t.Errorf("symbols: %q", r.Content)
	}
	if r := run(`{"operation":"workspace_symbols","query":"helper"}`); !strings.Contains(r.Content, "function helper  lib.fake:2") {
		t.Errorf("workspace_symbols: %+v", r)
	}
	if r := run(`{"operation":"definition","path":"main.fake"}`); !r.IsError {
		t.Error("definition without position should fail")
	}
	if !strings.Contains(tl.Spec().Description, ".fake") {
		t.Error("spec should list languages")
	}
}

func TestMissingBinaryAndOutsideRoot(t *testing.T) {
	dir := t.TempDir()
	m := NewManager(map[string]ServerConfig{
		"nope":  {Command: []string{"definitely-not-a-real-binary-xyz"}, Extensions: []string{".nope"}},
		"gopls": {Disabled: true}, "typescript": {Disabled: true}, "pyright": {Disabled: true}, "rust": {Disabled: true}, "clangd": {Disabled: true},
	}, dir, dir, "")
	if m.Enabled() {
		t.Error("uninstalled servers must be skipped")
	}
	if d := m.Diagnostics(context.Background(), "/elsewhere/x.go"); d != "" {
		t.Error("expected nothing")
	}
}

func TestHelpers(t *testing.T) {
	line := "héllo 🌍 x"
	if got := utf16Col(line, 8); got != 9 { // emoji is 2 UTF-16 units
		t.Errorf("utf16Col = %d", got)
	}
	if got := runeCol(line, 9); got != 8 {
		t.Errorf("runeCol = %d", got)
	}
	links := parseLocations(json.RawMessage(`[{"targetUri":"file:///a.go","targetSelectionRange":{"start":{"line":3,"character":1},"end":{"line":3,"character":2}}}]`))
	if len(links) != 1 || links[0].URI != "file:///a.go" || links[0].Range.Start.Line != 3 {
		t.Errorf("links = %+v", links)
	}
	if got := parseLocations(json.RawMessage(`{"uri":"file:///b.go","range":{"start":{"line":1,"character":0},"end":{"line":1,"character":1}}}`)); len(got) != 1 {
		t.Error("single location")
	}
	if got := hoverText(json.RawMessage(`{"contents":["a",{"language":"go","value":"b"}]}`)); got != "a\n\nb" {
		t.Errorf("hover = %q", got)
	}
	if uriToPath(pathToURI("/tmp/a b.go")) != "/tmp/a b.go" {
		t.Error("uri roundtrip")
	}
}

func TestGopls(t *testing.T) {
	if _, err := exec.LookPath("gopls"); err != nil || testing.Short() {
		t.Skip("gopls not installed")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, filepath.Join(dir, "go.mod"), "module example.com/demo\n\ngo 1.22\n")
	main := filepath.Join(dir, "main.go")
	write(t, main, "package main\n\nfunc greet() string { return \"hi\" }\n\nfunc main() { _ = greet() }\n")
	m := NewManager(nil, dir, dir, filepath.Join(dir, "logs"))
	defer m.Close()
	ctx := context.Background()

	start := time.Now()
	if d := m.Diagnostics(ctx, main); d != "" {
		t.Fatalf("clean file: %s", d)
	}
	t.Logf("first check (server start) took %s", time.Since(start))

	write(t, main, "package main\n\nfunc greet() string { return \"hi\" }\n\nfunc main() { _ = gret() }\n")
	start = time.Now()
	d := m.Diagnostics(ctx, main)
	if !strings.Contains(d, "ERROR 5:19") || !strings.Contains(d, "undefined: gret") {
		t.Fatalf("expected undefined error, got:\n%s", d)
	}
	t.Logf("edit check took %s", time.Since(start))

	write(t, main, "package main\n\nfunc greet() string { return \"hi\" }\n\nfunc main() { _ = greet() }\n")
	m.Diagnostics(ctx, main)
	r := Tool{M: m}.Run(ctx, tools.NewEnv(dir), json.RawMessage(`{"operation":"definition","path":"main.go","line":5,"column":19}`))
	if !strings.HasPrefix(r.Content, "main.go:3:6") {
		t.Errorf("definition = %+v", r)
	}
}

func TestPullDiagnostics(t *testing.T) {
	bin := buildFake(t)
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	m := NewManager(map[string]ServerConfig{
		"fake":  {Command: []string{bin}, Extensions: []string{".fake"}, Env: map[string]string{"FAKELS_PULL": "1"}},
		"gopls": {Disabled: true},
	}, dir, dir, filepath.Join(dir, "logs"))
	t.Cleanup(m.Close)
	path := filepath.Join(dir, "a.fake")
	ctx := context.Background()

	write(t, path, "def main\nok\n")
	start := time.Now()
	if d := m.Diagnostics(ctx, path); d != "" {
		t.Fatalf("clean file: %q", d)
	}
	write(t, path, "def main\nthis is BAD\n")
	if d := m.Diagnostics(ctx, path); !strings.Contains(d, "ERROR 2:9 found BAD") {
		t.Fatalf("a pull server's errors should be reported: %q", d)
	}
	// Pulling answers at once instead of waiting out the push timeout.
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("pull diagnostics took %s", took)
	}
}

func TestCodeActions(t *testing.T) {
	m, dir := fakeManager(t)
	path := filepath.Join(dir, "a.fake")
	ctx := context.Background()
	env := tools.NewEnv(dir)
	env.Diagnostics = m.Diagnostics
	list := func(line int) string {
		r := Tool{M: m}.Run(ctx, env, json.RawMessage(`{"operation":"code_actions","path":"a.fake","line":`+fmt.Sprint(line)+`}`))
		if r.IsError {
			t.Fatalf("code_actions: %s", r.Content)
		}
		return r.Content
	}
	apply := func(title string) tools.Result {
		in, _ := json.Marshal(map[string]any{"path": "a.fake", "line": 2, "title": title})
		return ApplyTool{M: m}.Run(ctx, env, in)
	}
	content := func() string { b, _ := os.ReadFile(path); return string(b) }

	write(t, path, "def main\nBAD and BAD\nok\n")
	got := list(2)
	for _, want := range []string{"1. Replace BAD with GOOD  [quickfix, preferred]", "2. Fix every BAD in the file  [source.fixAll]", "3. Fix with a command"} {
		if !strings.Contains(got, want) {
			t.Errorf("list lacks %q:\n%s", want, got)
		}
	}
	if got := list(3); !strings.Contains(got, "No code actions") {
		t.Errorf("clean line: %q", got)
	}

	r := apply("Replace BAD with GOOD")
	if r.IsError || content() != "def main\nGOOD and BAD\nok\n" || !strings.Contains(r.Display, "- BAD\n+ GOOD") || !strings.Contains(r.Content, "a.fake (1 edit)") {
		t.Fatalf("direct edit: %+v\n%q", r, content())
	}
	if !strings.Contains(r.Content, "found BAD") {
		t.Errorf("the result should carry the file's remaining diagnostics: %q", r.Content)
	}

	write(t, path, "def main\nBAD and BAD\nok\n")
	if r := apply("fix every bad in the file"); r.IsError || content() != "def main\nGOOD and GOOD\nok\n" {
		t.Fatalf("resolved action (and a case-insensitive title): %+v\n%q", r, content())
	}

	write(t, path, "def main\nBAD here\n")
	if r := apply("Fix with a command"); r.IsError || content() != "def main\nFIXED here\n" {
		t.Fatalf("command action: %+v\n%q", r, content())
	}

	write(t, path, "def main\nBAD here\n")
	if r := apply("Rewrite everything"); !r.IsError || !strings.Contains(r.Content, `"Replace BAD with GOOD"`) {
		t.Fatalf("unknown title: %+v", r)
	}
}

func TestServerCannotEditUnasked(t *testing.T) {
	m, dir := fakeManager(t)
	path := filepath.Join(dir, "a.fake")
	write(t, path, "SNEAKY\n")
	m.Diagnostics(context.Background(), path) // opens it; the server tries an edit
	time.Sleep(300 * time.Millisecond)
	if b, _ := os.ReadFile(path); string(b) != "SNEAKY\n" {
		t.Fatalf("an unrequested workspace/applyEdit changed the file: %q", b)
	}
}

func TestApplyTextEdits(t *testing.T) {
	out, _, err := applyTextEdits("héllo wörld\nsecond", []TextEdit{
		{Range: Range{Start: Position{0, 6}, End: Position{0, 11}}, NewText: "earth"},
		{Range: Range{Start: Position{1, 0}, End: Position{1, 6}}, NewText: "2nd"},
	})
	if err != nil || out != "héllo earth\n2nd" {
		t.Fatalf("got %q %v", out, err)
	}
	if _, _, err := applyTextEdits("abcdef", []TextEdit{
		{Range: Range{Start: Position{0, 0}, End: Position{0, 3}}, NewText: "x"},
		{Range: Range{Start: Position{0, 2}, End: Position{0, 4}}, NewText: "y"},
	}); err == nil {
		t.Fatal("overlapping edits must be refused")
	}
	if _, err := editsByFile(WorkspaceEdit{DocumentChanges: []json.RawMessage{json.RawMessage(`{"kind":"delete","uri":"file:///x"}`)}}); err == nil {
		t.Fatal("deleting files isn't supported")
	}
}
