package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadSearchesHideSecretPathsAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	secretDir := filepath.Join(dir, "credentials")
	if err := os.Mkdir(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(secretDir, "token.json")
	if err := os.WriteFile(secret, []byte("UNIQUE_SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "public.txt"), []byte("public UNIQUE_SECRET marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "credential-link")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}
	env := NewEnv(dir)
	env.SecretPaths = []string{secretDir}

	for _, path := range []string{secret, link} {
		in, _ := json.Marshal(map[string]string{"path": path})
		res := (Read{}).Run(context.Background(), env, in)
		if !res.IsError || strings.Contains(res.Content, "UNIQUE_SECRET") {
			t.Fatalf("read %s exposed secret: %#v", path, res)
		}
	}

	grep := (Grep{}).Run(context.Background(), env, json.RawMessage(`{"pattern":"UNIQUE_SECRET","path":"."}`))
	if grep.IsError || strings.Contains(grep.Content, "credentials") || strings.Contains(grep.Content, "credential-link") {
		t.Fatalf("recursive grep exposed a credential path: %#v", grep)
	}
	if !strings.Contains(grep.Content, "public.txt") {
		t.Fatalf("recursive grep lost public match: %#v", grep)
	}

	glob := (Glob{}).Run(context.Background(), env, json.RawMessage(`{"pattern":"**/*","path":"."}`))
	if glob.IsError || strings.Contains(glob.Content, "credentials") || strings.Contains(glob.Content, "credential-link") {
		t.Fatalf("glob exposed a credential path: %#v", glob)
	}
	if !strings.Contains(glob.Content, "public.txt") {
		t.Fatalf("glob lost public file: %#v", glob)
	}
}

func TestBashScrubsSecretEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("bash command is Unix-specific")
	}
	const name = "LARIK_TEST_PROVIDER_SECRET"
	t.Setenv(name, "should-not-leak")
	env := NewEnv(t.TempDir())
	env.SecretEnv = []string{name}
	res := (Bash{}).Run(context.Background(), env, json.RawMessage(`{"command":"if [ -z \"$LARIK_TEST_PROVIDER_SECRET\" ]; then echo scrubbed; else echo leaked; fi","sandbox":false}`))
	if res.IsError || !strings.Contains(res.Content, "scrubbed") || strings.Contains(res.Content, "leaked") {
		t.Fatalf("secret environment was inherited: %#v", res)
	}
}

func TestRipgrepSingleFileIncludesStructuredPath(t *testing.T) {
	rg := ripgrepPath()
	if rg == "" {
		t.Skip("ripgrep is not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "one.txt")
	if err := os.WriteFile(path, []byte("structured match\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines := newSearchLines(dir).forScript(true)
	if err := ripgrep(context.Background(), rg, path, grepInput{Pattern: "structured"}, lines); err != nil {
		t.Fatal(err)
	}
	data := grepData(lines)
	matches, ok := data["matches"].([]map[string]any)
	if !ok || len(matches) != 1 {
		t.Fatalf("structured matches = %#v", data["matches"])
	}
	if matches[0]["path"] != "one.txt" || matches[0]["line"] != 1 || matches[0]["text"] != "structured match" {
		t.Fatalf("structured match = %#v", matches[0])
	}
}

func TestFilteredSearchPreservesGitignoreAndNewlinePaths(t *testing.T) {
	if ripgrepPath() == "" {
		t.Skip("ripgrep needed")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{".gitignore": "ignored.txt\n", "ignored.txt": "needle\n", "public.txt": "needle\n", "secret.txt": "PRIVATE_TOKEN needle\n", "line\nbreak.txt": "needle\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	env := NewEnv(dir)
	env.SecretPaths = []string{filepath.Join(dir, "secret.txt")}
	result := (Grep{}).Run(context.Background(), env, json.RawMessage(`{"pattern":"needle"}`))
	if result.IsError || strings.Contains(result.Content, "ignored.txt") || strings.Contains(result.Content, "PRIVATE_TOKEN") || !strings.Contains(result.Content, "public.txt") || !strings.Contains(result.Content, "line\nbreak.txt") {
		t.Fatalf("filtered search = %+v", result)
	}
}

func TestExternalSecretsKeepRipgrepFastPath(t *testing.T) {
	if ripgrepPath() == "" {
		t.Skip("ripgrep needed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "public.txt"), []byte("needle"), 0600); err != nil {
		t.Fatal(err)
	}
	env := NewEnv(root)
	env.SecretPaths = []string{filepath.Join(t.TempDir(), "auth.json")}
	if env.searchNeedsFilter(root) {
		t.Fatal("external secret unnecessarily disables ripgrep")
	}
	// Extended-mode regex is supported by ripgrep, but not Go regexp.
	res := (Grep{}).Run(context.Background(), env, json.RawMessage(`{"pattern":"(?x) needle"}`))
	if res.IsError || !strings.Contains(res.Content, "public.txt") {
		t.Fatalf("ripgrep fast path lost: %+v", res)
	}
	env.SecretPaths = []string{filepath.Join(root, "auth.json")}
	if !env.searchNeedsFilter(root) {
		t.Fatal("in-tree secret failed to activate filtering")
	}
}
