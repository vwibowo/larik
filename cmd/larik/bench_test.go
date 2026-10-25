package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestBenchModelSpec(t *testing.T) {
	if got := benchModelSpec("main"); got != "" {
		t.Errorf("main resolved as %q, want the empty primary-model spec", got)
	}
	if got := benchModelSpec("worker"); got != "worker" {
		t.Errorf("worker resolved as %q", got)
	}
}

func TestCompactionChange(t *testing.T) {
	if got := compactionChange(16_000); got != "~16.0k context freed" {
		t.Errorf("positive change = %q", got)
	}
	if got := compactionChange(-500); got != "~500 context added" {
		t.Errorf("negative change = %q", got)
	}
}

func TestSpread(t *testing.T) {
	n := func(v int) string { return fmt.Sprint(v) }
	for _, c := range []struct {
		xs   []int
		want string
	}{
		{[]int{5}, "5"},
		{[]int{4, 4, 4}, "4"},
		{[]int{9, 1, 5}, "5 (1–9)"},
		{[]int{2, 8, 4, 6}, "5 (2–8)"},
	} {
		if got := spread(c.xs, n); got != c.want {
			t.Errorf("spread(%v) = %q, want %q", c.xs, got, c.want)
		}
	}
	if xs := []int{3, 1, 2}; spread(xs, n) != "2 (1–3)" || xs[0] != 3 {
		t.Error("spread must not reorder its input")
	}
}

func TestBenchHarnesses(t *testing.T) {
	if hs, err := benchHarnesses(""); err != nil || len(hs) != 1 || hs[0].dir != "" {
		t.Fatalf("no flag = %v, %v; want only the default harness", hs, err)
	}
	base := t.TempDir()
	a, b := filepath.Join(base, "terse"), filepath.Join(base, "verbose")
	for _, d := range []string{a, b, filepath.Join(base, "x", "terse")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	hs, err := benchHarnesses("none," + a + ", " + b)
	if err != nil {
		t.Fatal(err)
	}
	if len(hs) != 3 || hs[0].dir != "" || hs[1].name != "terse" || hs[2].dir != b {
		t.Errorf("harnesses = %+v", hs)
	}
	if _, err := benchHarnesses(filepath.Join(base, "missing")); err == nil {
		t.Error("a missing directory should be rejected")
	}
	if _, err := benchHarnesses(a + "," + filepath.Join(base, "x", "terse")); err == nil {
		t.Error("two harnesses with the same name would share result rows")
	}
}
