package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoGrepOrderedAndTruncatedAcrossBatches(t *testing.T) {
	dir := t.TempDir()
	for i := range 520 {
		name := fmt.Sprintf("f%04d.txt", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("hit\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := newSearchLines(dir)
	if err := goGrep(context.Background(), dir, grepInput{Pattern: "hit"}, out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String("more")), "\n")
	if out.n != maxSearchResults || !out.full || lines[0] != "f0000.txt:1:hit" || lines[maxSearchResults-1] != "f0499.txt:1:hit" || lines[len(lines)-1] != "... (more)" {
		t.Fatalf("unexpected order/cap: first=%q last=%q count=%d full=%t", lines[0], lines[len(lines)-1], out.n, out.full)
	}
}

func TestGoGrepLongLineAndBinaryHandling(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("x", 128*1024) + "needle\nlast needle\n"
	if err := os.WriteFile(filepath.Join(dir, "long.txt"), []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "binary.txt"), []byte("text\x00needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := newSearchLines(dir)
	if err := goGrep(context.Background(), dir, grepInput{Pattern: "needle"}, out); err != nil {
		t.Fatal(err)
	}
	got := out.String("more")
	if strings.Count(got, "needle") != 1 || !strings.Contains(got, "long.txt:2:last needle") || strings.Contains(got, "binary.txt") {
		t.Fatalf("unexpected long-line or binary results: %q", got)
	}
}

func BenchmarkGoGrepFirstMatches(b *testing.B) {
	dir := b.TempDir()
	for i := range 700 {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%04d.txt", i)), []byte("hit\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for range b.N {
		out := newSearchLines(dir)
		if err := goGrep(context.Background(), dir, grepInput{Pattern: "hit"}, out); err != nil {
			b.Fatal(err)
		}
	}
}
