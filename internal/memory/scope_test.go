package memory

import (
	"context"
	"encoding/json"
	"testing"
)

func TestToolDeleteDefaultsToProjectAndRejectsInvalidScope(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save(Note{Name: "shared", Description: "d", Body: "keep", Scope: ScopeUser}); err != nil {
		t.Fatal(err)
	}
	tool := Tool{S: s}
	for _, input := range []string{`{"action":"delete","name":"shared"}`, `{"action":"delete","name":"shared","scope":"invalid"}`} {
		if res := tool.Run(context.Background(), nil, json.RawMessage(input)); !res.IsError {
			t.Fatalf("user note deleted implicitly: %+v", res)
		}
		if note, ok := s.Get("shared", ScopeUser); !ok || note.Body != "keep" {
			t.Fatal("user note changed")
		}
	}
	if _, err := s.Save(Note{Name: "shared", Description: "d", Body: "project"}); err != nil {
		t.Fatal(err)
	}
	if res := tool.Run(context.Background(), nil, json.RawMessage(`{"action":"delete","name":"shared"}`)); res.IsError {
		t.Fatal(res.Content)
	}
	if _, ok := s.Get("shared", ScopeProject); ok {
		t.Fatal("project note was not deleted")
	}
	if _, ok := s.Get("shared", ScopeUser); !ok {
		t.Fatal("user note was also deleted")
	}
	if res := tool.Run(context.Background(), nil, json.RawMessage(`{"action":"delete","name":"shared","scope":"user"}`)); res.IsError {
		t.Fatal(res.Content)
	}
	if _, ok := s.Get("shared", ScopeUser); ok {
		t.Fatal("explicit user deletion failed")
	}
}
