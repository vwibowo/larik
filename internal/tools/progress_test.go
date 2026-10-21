package tools

import "testing"

func TestLastLines(t *testing.T) {
	for in, want := range map[string]string{
		"a\nb\nc\n": "b\nc",
		"a\nb":      "a\nb",
		"":          "",
		"only":      "only",
	} {
		if got := lastLines([]byte(in), 2); got != want {
			t.Errorf("lastLines(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTailWriterReports(t *testing.T) {
	var got []string
	w := &tailWriter{report: func(s string) { got = append(got, s) }}
	w.Write([]byte("one\ntwo\n"))
	w.Write([]byte("three\n")) // within progressEvery: not reported
	if len(got) != 1 || got[0] != "one\ntwo" {
		t.Fatalf("reports = %q", got)
	}
}
