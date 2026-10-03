package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindMatchesExactPrefixAndAmbiguity(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"alpha", "alphabet", "beta"} {
		if err := os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte("not a transcript\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "alpine.jsonl"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ input, want, errText string }{
		{"alpha", "alpha.jsonl", ""},
		{"alphab", "alphabet.jsonl", ""},
		{"al", "", "ambiguous"},
		{"missing", "", "no session"},
	} {
		path, err := Find(dir, tc.input)
		if tc.errText != "" {
			if err == nil || !strings.Contains(err.Error(), tc.errText) {
				t.Errorf("Find(%q) error = %v", tc.input, err)
			}
		} else if err != nil || filepath.Base(path) != tc.want {
			t.Errorf("Find(%q) = %q, %v", tc.input, path, err)
		}
	}
}

func BenchmarkFindExact(b *testing.B) {
	dir := b.TempDir()
	for i := range 300 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("session-%04d.jsonl", i)), []byte(`{"type":"meta"}`+"\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for range b.N {
		if _, err := Find(dir, "session-0299"); err != nil {
			b.Fatal(err)
		}
	}
}
