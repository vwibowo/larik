package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// salvageTags are the delimiters models write around a tool call when the
// server did not convert it: the Qwen and Hermes templates emit the first,
// and several fine-tunes the second.
//
// Only explicitly delimited calls are salvaged. A bare or fenced JSON object
// is deliberately left alone: a model answering a question about JSON, or
// quoting a tool call it is explaining rather than making, would otherwise
// have it executed.
var salvageTags = [][2]string{
	{"<tool_call>", "</tool_call>"},
	{"<function_call>", "</function_call>"},
}

// salvaged is one tool call parsed out of a model's text. Templates disagree
// about the arguments key, and some write the arguments as a JSON string
// rather than an object.
type salvaged struct {
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments"`
	Parameters json.RawMessage `json:"parameters"`
}

// SalvageToolCalls finds tool calls a model wrote into its text instead of
// emitting them through the API's own tool-call field, which happens when a
// local server does not apply the model's tool template. It returns the
// recovered calls and the text with their markup removed, so what reaches
// the transcript is the prose the model meant rather than leaked markup.
//
// A call is only recovered when its name is one of specs: a model inventing
// a tool, or writing an example call for a tool that was never offered, must
// not cause one to run. Recovered calls go through the agent's ordinary
// permission checks like any other, so this cannot bypass a rule.
//
// Callers apply it only when the response carried no structured tool calls.
func SalvageToolCalls(text string, specs []ToolSpec) (calls []Block, cleaned string, found bool) {
	if text == "" || len(specs) == 0 || !strings.Contains(text, "<") {
		return nil, text, false
	}
	offered := make(map[string]bool, len(specs))
	for _, s := range specs {
		offered[s.Name] = true
	}

	cleaned = text
	for _, tag := range salvageTags {
		openTag, closeTag := tag[0], tag[1]
		for {
			i := strings.Index(cleaned, openTag)
			if i < 0 {
				break
			}
			j := strings.Index(cleaned[i+len(openTag):], closeTag)
			if j < 0 {
				break // an unterminated tag: a truncated response, not a call
			}
			body := cleaned[i+len(openTag) : i+len(openTag)+j]
			rest := cleaned[i+len(openTag)+j+len(closeTag):]

			call, ok := parseSalvaged(body, offered)
			if !ok {
				// Leave an unusable tag in place rather than silently
				// deleting text the user would want to see.
				return calls, cleaned, len(calls) > 0
			}
			calls = append(calls, call)
			cleaned = strings.TrimRight(cleaned[:i], " \t") + rest
		}
	}
	if len(calls) == 0 {
		return nil, text, false
	}
	return calls, strings.TrimSpace(cleaned), true
}

// parseSalvaged reads one delimited call body, accepting the argument keys
// and the stringified arguments that different templates produce.
func parseSalvaged(body string, offered map[string]bool) (Block, bool) {
	var s salvaged
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &s); err != nil {
		return Block{}, false
	}
	if s.Name == "" || !offered[s.Name] {
		return Block{}, false
	}
	args := s.Arguments
	if len(args) == 0 {
		args = s.Parameters
	}
	if len(args) == 0 || string(args) == "null" {
		args = json.RawMessage(`{}`)
	}
	// Some templates write the arguments as a JSON string.
	var asString string
	if json.Unmarshal(args, &asString) == nil && json.Valid([]byte(asString)) {
		args = json.RawMessage(asString)
	}
	// Whatever the shape said, the arguments have to be an object.
	var obj map[string]any
	if json.Unmarshal(args, &obj) != nil {
		return Block{}, false
	}
	return Block{Type: BlockToolUse, ID: NewCallID("call_"), Name: s.Name, Input: args}, true
}

// SalvageNotice is what the user is told when a call was recovered. A
// salvage is never silent: it means the server is not converting the
// model's tool calls, which is worth fixing where it happens.
func SalvageNotice(n int) string {
	calls := "a tool call"
	if n > 1 {
		calls = fmt.Sprintf("%d tool calls", n)
	}
	return "recovered " + calls + " from the model's text: this server isn't converting them, so check its tool-call support"
}

// SalvageGate streams text while holding back anything that may turn out to
// be tool-call markup, so a call the server failed to convert is never
// displayed. Prose before the markup streams as it arrives; from the moment
// an opening tag appears, nothing further is shown, because everything after
// it belongs to the call.
//
// It also withholds a trailing fragment that could still grow into an
// opening tag, so "<tool" is never shown on its way to "<tool_call>". A
// fragment that turns out to be ordinary text is released on the next
// delta, or by Flush at the end of the stream.
type SalvageGate struct {
	buf     strings.Builder
	shown   int  // bytes of buf already displayed
	stopped bool // an opening tag has been seen
}

// Write records a delta and returns the text that is safe to display now,
// which may be empty.
func (g *SalvageGate) Write(delta string) string {
	g.buf.WriteString(delta)
	if g.stopped {
		return ""
	}
	full := g.buf.String()
	safe := len(full)
	if i := indexAnyOpenTag(full); i >= 0 {
		safe, g.stopped = i, true
	} else {
		safe -= partialOpenTagSuffix(full)
	}
	if safe <= g.shown {
		return ""
	}
	out := full[g.shown:safe]
	g.shown = safe
	return out
}

// Flush returns anything held back that never became markup, for the end of
// a stream. It is empty once an opening tag has been seen.
func (g *SalvageGate) Flush() string {
	if g.stopped {
		return ""
	}
	full := g.buf.String()
	if len(full) <= g.shown {
		return ""
	}
	out := full[g.shown:]
	g.shown = len(full)
	return out
}

// Text is everything written, markup included, for SalvageToolCalls to read.
func (g *SalvageGate) Text() string { return g.buf.String() }

// Len is how much text has been written.
func (g *SalvageGate) Len() int { return g.buf.Len() }

// indexAnyOpenTag is the offset of the earliest opening tag, or -1.
func indexAnyOpenTag(s string) int {
	best := -1
	for _, tag := range salvageTags {
		if i := strings.Index(s, tag[0]); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// partialOpenTagSuffix is the length of the longest suffix of s that is a
// proper prefix of an opening tag, so it can be held back until the next
// delta settles what it is.
func partialOpenTagSuffix(s string) int {
	for _, tag := range salvageTags {
		open := tag[0]
		for n := min(len(open)-1, len(s)); n > 0; n-- {
			if strings.HasSuffix(s, open[:n]) {
				return n
			}
		}
	}
	return 0
}
