package tui

import (
	"strings"
	"testing"
	"time"

	"larik/internal/termbrowser"
)

func TestReplayNeedsTheBrowser(t *testing.T) {
	m := testModel(t)
	msg := m.replayCommand("")()
	if out, ok := msg.(outputMsg); !ok || !strings.Contains(string(out), "browser tools are off") {
		t.Fatalf("%v", msg)
	}
}

func TestCompareNeedsTheBrowser(t *testing.T) {
	m := testModel(t)
	msg := m.compareCommand()()
	if out, ok := msg.(outputMsg); !ok || !strings.Contains(string(out), "browser tools are off") {
		t.Fatalf("%v", msg)
	}
}

func TestRewindEntriesPairPromptsWithPictures(t *testing.T) {
	at := func(n int) time.Time { return time.Unix(int64(n), 0) }
	thumbs := []termbrowser.Thumb{{JPEG: []byte("a"), At: at(25)}, {JPEG: []byte("b"), At: at(45)}}
	// Prompts at 10, 20 and 30 and 40, then 50 which has no edit yet.
	times := []time.Time{at(10), at(20), at(30), at(40), at(50)}
	got := rewindEntries(thumbs, times, 0)
	if len(got) != 2 || got[0].Label != "1-2" || string(got[0].JPEG) != "a" || got[1].Label != "3-4" || string(got[1].JPEG) != "b" {
		t.Fatalf("%+v", got)
	}
	if got := rewindEntries(thumbs, times, 1); got[0].Label != "2" {
		t.Fatalf("the list started at prompt 2: %+v", got)
	}
	if got := rewindEntries(nil, times, 0); got != nil {
		t.Fatalf("an earlier run's prompts have none: %+v", got)
	}
}
