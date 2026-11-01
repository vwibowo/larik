package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestWillWriteHearsOfAWrite(t *testing.T) {
	env := NewEnv(t.TempDir())
	calls := 0
	env.WillWrite = func() { calls++ }
	input, _ := json.Marshal(map[string]string{"path": filepath.Join(env.Cwd, "a.css"), "content": "a{}"})
	if res := (Write{}).Run(context.Background(), env, input); res.IsError {
		t.Fatal(res.Content)
	}
	if calls != 1 {
		t.Fatalf("%d calls", calls)
	}
}
