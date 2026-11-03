package terminal

import "testing"

func TestStatusOSCStream(t *testing.T) {
	var s statusOSC
	if got := s.scan([]byte("before\x1b]7501;state=wor")); len(got) != 0 {
		t.Fatalf("early event: %q", got)
	}
	if got := s.scan([]byte("king\x07\x1b]9;hello\x07\x1b]7501;state=done\x1b")); len(got) != 1 || got[0].payload != "state=working" {
		t.Fatalf("BEL event: %q", got)
	}
	if got := s.scan([]byte("\\")); len(got) != 1 || got[0].payload != "state=done" {
		t.Fatalf("ST event: %q", got)
	}
	if got := s.scan([]byte("\x1b]7501;" + string(make([]byte, 4100)) + "\x07")); len(got) != 0 {
		t.Fatalf("oversized event: %q", got)
	}
	if got := s.scan([]byte("\x1b]133;A\x07\x1bc")); len(got) != 2 || got[0].kind != 'P' || got[1].kind != 'R' {
		t.Fatalf("prompt and reset: %+v", got)
	}
}
