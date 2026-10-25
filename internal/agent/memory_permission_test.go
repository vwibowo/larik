package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync/atomic"
	"testing"

	"larik/internal/llm"
	"larik/internal/memory"
	"larik/internal/permission"
	"larik/internal/tools"
)

func TestUserMemoryWritesNeedHumanApproval(t *testing.T) {
	for _, mode := range []permission.Mode{permission.ModeDefault, permission.ModeAcceptEdits, permission.ModePlan, permission.ModeAuto} {
		for _, action := range []string{"save", "delete"} {
			for _, allow := range []bool{false, true} {
				t.Run(string(mode)+"/"+action+"/"+map[bool]string{true: "allow", false: "deny"}[allow], func(t *testing.T) {
					dir := t.TempDir()
					store := memory.New(filepath.Join(dir, "project-memory"), filepath.Join(dir, "user-memory"))
					if _, err := store.Save(memory.Note{Name: "shared", Description: "existing", Body: "original", Scope: memory.ScopeUser}); err != nil {
						t.Fatal(err)
					}
					input, _ := json.Marshal(map[string]string{"action": action, "scope": "user", "name": "shared", "description": "updated", "content": "replacement"})
					fp := &fakeProvider{script: []llm.Message{assistant(toolUse("mem", "memory", string(input))), assistant(llm.TextBlock("done"))}}
					a := New(Options{Provider: fp, Model: "m", Cwd: dir, Tools: tools.NewRegistry(memory.Tool{S: store}), Perms: permission.NewChecker(mode, permission.Rules{}, dir)})
					var classified atomic.Int64
					a.SetAutoApprover(func(context.Context, AutoCall) (AutoVerdict, error) {
						classified.Add(1)
						return AutoVerdict{Allow: true}, nil
					})
					ev := drain(a.Run(context.Background(), "update the note"), PermissionReply{Allow: allow})
					if len(permissionEvents(ev)) != 1 {
						t.Fatalf("wanted one user prompt: %+v", ev)
					}
					if classified.Load() != 0 {
						t.Fatal("classifier was asked to authorize cross-project memory")
					}
					note, exists := store.Get("shared", memory.ScopeUser)
					if !allow && (!exists || note.Body != "original") {
						t.Fatal("denied operation changed memory")
					}
					if allow && action == "save" && (!exists || note.Body != "replacement") {
						t.Fatal("approved save failed")
					}
					if allow && action == "delete" && exists {
						t.Fatal("approved delete failed")
					}
				})
			}
		}
	}
}

func TestUserMemoryNotPrefetchedOrAutomaticallyApproved(t *testing.T) {
	a, _, _ := setup(t, permission.ModeAuto)
	var classified atomic.Int64
	a.SetAutoApprover(func(context.Context, AutoCall) (AutoVerdict, error) {
		classified.Add(1)
		return AutoVerdict{Allow: true}, nil
	})
	input := json.RawMessage(`{"action":"save","scope":"user","name":"x"}`)
	registry := tools.NewRegistry(memory.Tool{})
	uses := []llm.Block{toolUse("a", "memory", string(input)), toolUse("b", "memory", string(input))}
	if pending := a.prefetchAuto(context.Background(), uses, registry); len(pending) != 0 {
		t.Fatal("user memory classified ahead of approval")
	}
	if verdict, err := a.autoApprove(context.Background(), "memory", input); err != nil || verdict.Allow {
		t.Fatalf("automatic approval accepted user memory: %+v %v", verdict, err)
	}
	if classified.Load() != 0 {
		t.Fatal("classifier called")
	}
}
