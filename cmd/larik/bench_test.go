package main

import (
	"fmt"
	"testing"
)

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
