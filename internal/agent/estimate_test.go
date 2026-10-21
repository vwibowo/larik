package agent

import (
	"strings"
	"testing"

	"larik/internal/llm"
)

func TestMessageEstimateTracksTranscript(t *testing.T) {
	msg := func(s string) llm.Message {
		return llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: s}}}
	}
	a := &Agent{}
	check := func(when string) {
		t.Helper()
		if got, want := a.messageEstimateLocked(), estimateMessages(a.messages); got != want {
			t.Fatalf("%s: running estimate %d, full %d", when, got, want)
		}
	}
	for i := 0; i < 5; i++ {
		a.messages = append(a.messages, msg(strings.Repeat("x", 40*(i+1))))
		check("append")
	}
	a.messages[len(a.messages)-1].Blocks[0].Text += strings.Repeat("y", 400)
	check("last message grew")
	a.messages = append(a.messages[:2], msg(strings.Repeat("z", 4000)), msg("q"), msg("r"))
	check("rewind then append")
	a.messages = []llm.Message{msg("summary")}
	check("replaced")
}
