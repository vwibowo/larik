package lsp

import (
	"context"
	"encoding/json"
	"fmt"
	"larik/internal/tools"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceEditPreflightsAllDestinations(t *testing.T) {
	m, dir := fakeManager(t)
	a, z := filepath.Join(dir, "a.fake"), filepath.Join(dir, "z.fake")
	write(t, a, "MULTIFILE\nBAD\n")
	write(t, z, "BAD\n")
	ctx := tools.WithWriteCheck(context.Background(), func(path string) error {
		if path == z {
			return fmt.Errorf("denied destination")
		}
		return nil
	})
	input := json.RawMessage(`{"path":"a.fake","line":2,"title":"Fix two files"}`)
	res := (ApplyTool{M: m}).Run(ctx, tools.NewEnv(dir), input)
	if !res.IsError || !strings.Contains(res.Content, "denied destination") {
		t.Fatalf("result = %+v", res)
	}
	for path, want := range map[string]string{a: "MULTIFILE\nBAD\n", z: "BAD\n"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("%s changed: %q (%v)", path, got, err)
		}
	}
}

func TestCommandWorkspaceEditChecksDestination(t *testing.T) {
	m, dir := fakeManager(t)
	path := filepath.Join(dir, "a.fake")
	write(t, path, "def main\nBAD here\n")
	ctx := tools.WithWriteCheck(context.Background(), func(string) error { return fmt.Errorf("denied command edit") })
	res := (ApplyTool{M: m}).Run(ctx, tools.NewEnv(dir), json.RawMessage(`{"path":"a.fake","line":2,"title":"Fix with a command"}`))
	got, _ := os.ReadFile(path)
	if string(got) != "def main\nBAD here\n" || !res.IsError {
		t.Fatalf("result = %+v, file = %q", res, got)
	}
}
