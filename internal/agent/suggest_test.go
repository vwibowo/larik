package agent

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"larik/internal/llm"
	"larik/internal/permission"
)

func TestSuggestNextReusesThePrefixAndLeavesTheTranscript(t *testing.T) {
	pricedModel(t, "m", 1000, 0)
	a, fp, _ := setup(t, permission.ModeYolo,
		assistant(llm.TextBlock("The tests are written.")),
		assistant(llm.TextBlock("\"run the tests\"")),
	)
	drain(a.Run(context.Background(), "write tests for the parser"), PermissionReply{})
	before, _ := os.ReadFile(a.SessionPath())
	a.mu.Lock()
	msgs := append([]llm.Message(nil), a.messages...)
	ctxBefore, costBefore := a.lastContext, a.cost
	a.mu.Unlock()

	got, err := a.SuggestNext(context.Background())
	if err != nil || got != "run the tests" {
		t.Fatalf("suggestion %q err %v", got, err)
	}
	if len(fp.requests) != 2 {
		t.Fatalf("requests = %d", len(fp.requests))
	}
	turn, sugg := fp.requests[0], fp.requests[1]
	if sugg.System != turn.System || sugg.CacheKey != turn.CacheKey || sugg.Model != turn.Model || !reflect.DeepEqual(sugg.Tools, turn.Tools) {
		t.Error("the suggestion request should share the conversation's cached prefix")
	}
	if n := len(sugg.Messages); n != len(msgs)+1 || !reflect.DeepEqual(sugg.Messages[:n-1], msgs) {
		t.Fatalf("suggestion request messages should be the transcript plus one question: got %d, transcript %d", n, len(msgs))
	}
	if !strings.Contains(sugg.Messages[len(sugg.Messages)-1].Text(), "Predict what the user will most likely type next") {
		t.Error("the last message should ask for a suggestion")
	}

	a.mu.Lock()
	after := append([]llm.Message(nil), a.messages...)
	ctxAfter, costAfter := a.lastContext, a.cost
	a.mu.Unlock()
	if !reflect.DeepEqual(after, msgs) {
		t.Error("the conversation must not change")
	}
	if ctxAfter != ctxBefore {
		t.Errorf("measured context changed: %d -> %d", ctxBefore, ctxAfter)
	}
	if costAfter <= costBefore {
		t.Errorf("the suggestion's spend should be counted: %v -> %v", costBefore, costAfter)
	}
	data, _ := os.ReadFile(a.SessionPath())
	added := string(data[len(before):])
	if strings.Contains(added, "Predict what the user") || strings.Contains(added, "run the tests") {
		t.Errorf("the transcript should get only a usage entry, got %s", added)
	}
	if !strings.Contains(added, `"usage"`) {
		t.Errorf("the spend should be saved with the session, got %q", added)
	}
}

func TestSuggestNextSkips(t *testing.T) {
	t.Run("empty conversation", func(t *testing.T) {
		a, fp, _ := setup(t, permission.ModeYolo)
		if got, err := a.SuggestNext(context.Background()); got != "" || err != nil || len(fp.requests) != 0 {
			t.Fatalf("got %q err %v requests %d", got, err, len(fp.requests))
		}
	})
	t.Run("pending tool call", func(t *testing.T) {
		a, fp, _ := setup(t, permission.ModeYolo)
		a.messages = []llm.Message{llm.UserText("hi"), assistant(toolUse("t", "glob", `{}`))}
		if got, _ := a.SuggestNext(context.Background()); got != "" || len(fp.requests) != 0 {
			t.Fatalf("got %q requests %d", got, len(fp.requests))
		}
	})
	t.Run("budget spent", func(t *testing.T) {
		a, fp, _ := setup(t, permission.ModeYolo)
		a.messages = []llm.Message{llm.UserText("hi"), assistant(llm.TextBlock("hello"))}
		a.cost = 1
		a.opts.Budget = func() (float64, float64) { return 1, 0.8 }
		if got, _ := a.SuggestNext(context.Background()); got != "" || len(fp.requests) != 0 {
			t.Fatalf("got %q requests %d", got, len(fp.requests))
		}
	})
	t.Run("whole-turn runtime", func(t *testing.T) {
		a, fp, _ := setup(t, permission.ModeYolo)
		a.messages = []llm.Message{llm.UserText("hi"), assistant(llm.TextBlock("hello"))}
		a.opts.Runtime = scriptedRuntime{}
		if got, _ := a.SuggestNext(context.Background()); got != "" || len(fp.requests) != 0 {
			t.Fatalf("got %q requests %d", got, len(fp.requests))
		}
	})
}

func TestParseSuggestion(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"run the tests", "run the tests"},
		{"  \"commit this\"  ", "commit this"},
		{"“now the server”", "now the server"},
		{"User: fix the lint errors", "fix the lint errors"},
		{"add a test\nbecause the parser changed", "add a test"},
		{"NONE", ""},
		{"none.", ""},
		{"", ""},
		{"/clear", ""},
		{"!rm -rf build", ""},
		{strings.Repeat("word ", 40), ""},
	} {
		if got := parseSuggestion(tc.in); got != tc.want {
			t.Errorf("parseSuggestion(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
