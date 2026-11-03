package osc

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf8"

	"larik/internal/status"
)

func TestStatus(t *testing.T) {
	got := Status(status.Report{State: status.Blocked, Kind: status.Permission, Title: "fix tests", Msg: "needs\nbash"})
	want := "\x1b]7501;id=larik:app=larik:state=blocked:kind=permission" +
		":title=" + base64.StdEncoding.EncodeToString([]byte("fix tests")) +
		":msg=" + base64.StdEncoding.EncodeToString([]byte("needs bash")) + "\x07"
	if got != want {
		t.Fatalf("Status = %q, want %q", got, want)
	}
	// A kind means something only while blocked.
	if got := Status(status.Report{State: status.Working, Kind: status.Question}); strings.Contains(got, "kind=") {
		t.Fatalf("working report carries a kind: %q", got)
	}
}

func TestStatusTextLimits(t *testing.T) {
	long := strings.Repeat("é", 200) // 400 bytes
	enc := statusText(long, maxStatusTitle)
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxStatusTitle || !utf8.Valid(raw) {
		t.Fatalf("title is %d bytes, valid UTF-8 %v", len(raw), utf8.Valid(raw))
	}
	if statusText(" \t\n", maxStatusMsg) != "" {
		t.Fatal("blank text should be left out")
	}
	if raw, _ := base64.StdEncoding.DecodeString(statusText("a\u0085b\x1bc", maxStatusMsg)); string(raw) != "a b c" {
		t.Fatalf("control characters kept: %q", raw)
	}
}
