package textutil

import "testing"

func TestHelpers(t *testing.T) {
	if got := FirstLine("a\nb"); got != "a" {
		t.Errorf("FirstLine = %q", got)
	}
	if got := FirstText("\n  \n## Title  \nbody"); got != "Title" {
		t.Errorf("FirstText = %q", got)
	}
	if got := OneLine("a\n  b\tc", 0); got != "a b c" {
		t.Errorf("OneLine = %q", got)
	}
	if got := OneLine("héllo world", 5); got != "héll…" {
		t.Errorf("OneLine clip = %q", got)
	}
	if Plural(1, "file") != "1 file" || Plural(2, "file") != "2 files" {
		t.Error("Plural")
	}
}
