package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

var salvageSpecs = []ToolSpec{
	{Name: "read", Schema: json.RawMessage(`{"type":"object"}`)},
	{Name: "bash", Schema: json.RawMessage(`{"type":"object"}`)},
}

func TestSalvageRecoversADelimitedCall(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		wantTool   string
		wantInput  string
		wantText   string
	}{
		{
			name:     "the Qwen and Hermes form",
			text:     `<tool_call>{"name": "read", "arguments": {"path": "a.go"}}</tool_call>`,
			wantTool: "read", wantInput: `{"path": "a.go"}`, wantText: "",
		},
		{
			name:     "a function_call tag",
			text:     `<function_call>{"name":"bash","arguments":{"command":"ls"}}</function_call>`,
			wantTool: "bash", wantInput: `{"command":"ls"}`, wantText: "",
		},
		{
			name:     "the parameters key instead of arguments",
			text:     `<tool_call>{"name":"read","parameters":{"path":"b.go"}}</tool_call>`,
			wantTool: "read", wantInput: `{"path":"b.go"}`, wantText: "",
		},
		{
			name:     "arguments written as a JSON string",
			text:     `<tool_call>{"name":"read","arguments":"{\"path\":\"c.go\"}"}</tool_call>`,
			wantTool: "read", wantInput: `{"path":"c.go"}`, wantText: "",
		},
		{
			name:     "no arguments at all",
			text:     `<tool_call>{"name":"bash"}</tool_call>`,
			wantTool: "bash", wantInput: `{}`, wantText: "",
		},
		{
			name:     "prose around the call is kept",
			text:     "Let me look at that file.\n<tool_call>{\"name\":\"read\",\"arguments\":{\"path\":\"a.go\"}}</tool_call>",
			wantTool: "read", wantInput: `{"path":"a.go"}`, wantText: "Let me look at that file.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, cleaned, found := SalvageToolCalls(tc.text, salvageSpecs)
			if !found || len(calls) != 1 {
				t.Fatalf("found=%v calls=%+v", found, calls)
			}
			if calls[0].Name != tc.wantTool {
				t.Errorf("tool = %q, want %q", calls[0].Name, tc.wantTool)
			}
			if string(calls[0].Input) != tc.wantInput {
				t.Errorf("input = %s, want %s", calls[0].Input, tc.wantInput)
			}
			if cleaned != tc.wantText {
				t.Errorf("cleaned = %q, want %q", cleaned, tc.wantText)
			}
			if calls[0].Type != BlockToolUse || calls[0].ID == "" {
				t.Errorf("block = %+v", calls[0])
			}
		})
	}
}

func TestSalvageRecoversSeveralCalls(t *testing.T) {
	text := `<tool_call>{"name":"read","arguments":{"path":"a"}}</tool_call><tool_call>{"name":"bash","arguments":{"command":"ls"}}</tool_call>`
	calls, cleaned, found := SalvageToolCalls(text, salvageSpecs)
	if !found || len(calls) != 2 {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[0].Name != "read" || calls[1].Name != "bash" {
		t.Errorf("order not preserved: %s, %s", calls[0].Name, calls[1].Name)
	}
	if calls[0].ID == calls[1].ID {
		t.Error("each call needs its own id")
	}
	if cleaned != "" {
		t.Errorf("cleaned = %q", cleaned)
	}
}

// The safety boundary: anything that is not unambiguously a call the model
// is making must not become one.
func TestSalvageRefusesWhatIsNotACall(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"a tool that was never offered", `<tool_call>{"name":"rm_rf","arguments":{"path":"/"}}</tool_call>`},
		{"no name", `<tool_call>{"arguments":{"path":"a"}}</tool_call>`},
		{"not JSON", `<tool_call>read a.go please</tool_call>`},
		{"arguments that are not an object", `<tool_call>{"name":"read","arguments":[1,2]}</tool_call>`},
		{"an unterminated tag, i.e. a truncated reply", `<tool_call>{"name":"read","arguments":{"path":"a"}}`},
		{"bare JSON with no tag", `{"name":"read","arguments":{"path":"a.go"}}`},
		{"a fenced JSON example", "```json\n{\"name\":\"read\",\"arguments\":{\"path\":\"a.go\"}}\n```"},
		{"ordinary prose", "I read the file and it looks fine."},
		{"empty", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls, cleaned, found := SalvageToolCalls(tc.text, salvageSpecs)
			if found || len(calls) != 0 {
				t.Errorf("salvaged %+v from %q", calls, tc.text)
			}
			if cleaned != tc.text {
				t.Errorf("text must be left alone, got %q", cleaned)
			}
		})
	}
}

// Without tools on offer there is nothing a call could name, so nothing is
// recovered however the text looks.
func TestSalvageNeedsToolsOnOffer(t *testing.T) {
	text := `<tool_call>{"name":"read","arguments":{"path":"a"}}</tool_call>`
	if calls, cleaned, found := SalvageToolCalls(text, nil); found || calls != nil || cleaned != text {
		t.Errorf("found=%v calls=%+v", found, calls)
	}
}

// A usable call followed by an unusable tag keeps the call and leaves the
// rest of the text visible rather than deleting it.
func TestSalvageKeepsTextItCannotParse(t *testing.T) {
	text := `<tool_call>{"name":"read","arguments":{"path":"a"}}</tool_call> then <tool_call>nonsense</tool_call>`
	calls, cleaned, found := SalvageToolCalls(text, salvageSpecs)
	if !found || len(calls) != 1 || calls[0].Name != "read" {
		t.Fatalf("calls = %+v", calls)
	}
	if !strings.Contains(cleaned, "nonsense") {
		t.Errorf("unparsable markup should stay visible, got %q", cleaned)
	}
}

// shown replays deltas through a gate and returns what a user would have
// seen, plus everything the gate kept.
func shown(deltas ...string) (display, all string) {
	var g SalvageGate
	var b strings.Builder
	for _, d := range deltas {
		b.WriteString(g.Write(d))
	}
	b.WriteString(g.Flush())
	return b.String(), g.Text()
}

func TestGateStreamsProseButNeverTheMarkup(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deltas      []string
		wantDisplay string
	}{
		{
			name:        "markup arriving whole",
			deltas:      []string{"Reading it.\n", `<tool_call>{"name":"read"}</tool_call>`},
			wantDisplay: "Reading it.\n",
		},
		{
			name:        "the tag split across deltas",
			deltas:      []string{"Reading it.", "<tool", "_call>", `{"name":"read"}`, "</tool_call>"},
			wantDisplay: "Reading it.",
		},
		{
			name:        "one character at a time",
			deltas:      strings.Split("hi <tool_call>{}</tool_call>", ""),
			wantDisplay: "hi ",
		},
		{
			name:        "prose that merely starts like a tag",
			deltas:      []string{"use <tool", "box> for this"},
			wantDisplay: "use <toolbox> for this",
		},
		{
			name:        "a lone angle bracket is released",
			deltas:      []string{"a < b"},
			wantDisplay: "a < b",
		},
		{
			name:        "ordinary prose is untouched",
			deltas:      []string{"The file ", "looks fine."},
			wantDisplay: "The file looks fine.",
		},
		{
			name:        "nothing after the tag ever shows",
			deltas:      []string{"<tool_call>", `{"name":"read"}`, "</tool_call>", " trailing words"},
			wantDisplay: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			display, all := shown(tc.deltas...)
			if display != tc.wantDisplay {
				t.Errorf("displayed %q, want %q", display, tc.wantDisplay)
			}
			// Whatever is withheld, the gate still has the whole text for
			// salvaging.
			if want := strings.Join(tc.deltas, ""); all != want {
				t.Errorf("Text() = %q, want %q", all, want)
			}
		})
	}
}

// What the gate shows and what salvage leaves behind must agree, or the
// user would see prose the transcript lacks, or the reverse.
func TestGateAndSalvageAgreeOnWhatTheUserSees(t *testing.T) {
	deltas := []string{"Let me look.", `<tool_call>{"name":"read","arguments":{"path":"a"}}</tool_call>`}
	display, all := shown(deltas...)
	calls, cleaned, found := SalvageToolCalls(all, salvageSpecs)
	if !found || len(calls) != 1 {
		t.Fatalf("salvage found %v %+v", found, calls)
	}
	if cleaned != display {
		t.Errorf("transcript %q but the user saw %q", cleaned, display)
	}
}
