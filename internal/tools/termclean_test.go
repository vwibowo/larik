package tools

import (
	"strings"
	"testing"
)

func TestCleanTerminal(t *testing.T) {
	for in, want := range map[string]string{
		"plain\ntext":                               "plain\ntext",
		"\x1b[1;31mFAIL\x1b[0m pkg":                 "FAIL pkg",
		"\x1b]8;;http://x\x07link\x1b]8;;\x07 done": "link done",
		"10%\r50%\r100%\ndone":                      "100%\ndone",
		"windows\r\nlines\r\n":                      "windows\nlines\n",
		"spinner |\rspinner /\r":                    "spinner /",
		"\x1b[2K\rDownloading 3/3\x1b[0m\nok":       "Downloading 3/3\nok",
		"\x1b(Bcharset":                             "charset",
	} {
		if got := cleanTerminal(in); got != want {
			t.Errorf("cleanTerminal(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCapBuffer(t *testing.T) {
	var small capBuffer
	small.Write([]byte("hello "))
	small.Write([]byte("world"))
	if small.String() != "hello world" {
		t.Fatalf("small output must pass through: %q", small.String())
	}

	var big capBuffer
	chunk := []byte(strings.Repeat("0123456789", 100) + "\n")
	big.Write([]byte("FIRST\n"))
	for range 50_000 { // ~50 MB
		big.Write(chunk)
	}
	big.Write([]byte("LAST\n"))
	if len(big.head)+len(big.tail) > 3*captureBytes {
		t.Fatalf("buffer grew to %d bytes", len(big.head)+len(big.tail))
	}
	out := big.String()
	if !strings.HasPrefix(out, "FIRST\n") || !strings.HasSuffix(out, "LAST\n") || !strings.Contains(out, "bytes truncated") {
		t.Fatalf("head/tail lost: %q ... %q", out[:20], out[len(out)-20:])
	}
	if len(out) > MaxOutputBytes || Truncate(out, MaxOutputBytes) != out {
		t.Fatalf("overflowed output (%d bytes) must already fit the tool budget", len(out))
	}
}

func TestBashOutputIsQuietAndClean(t *testing.T) {
	env := NewEnv(t.TempDir())
	r := run(t, Bash{}, env, `{"command":"printf '\\033[32mok\\033[0m %s %s\\n' \"$NO_COLOR\" \"$GIT_PAGER\"; printf '1%%\\r100%%\\n'"}`)
	if r.Content != "ok 1 cat\n100%\n" {
		t.Fatalf("got %q", r.Content)
	}
}
