package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"larik/internal/llm"
	"larik/internal/session"
	"larik/internal/trace"
)

// A conversation can outgrow the model that summarizes it: a resumed
// transcript that was never measured, a huge tool result, a summarizer with
// a smaller window than the conversation's model, or simply a context the
// estimate (which errs low) let grow past the window. Sending it whole then
// fails exactly when compaction is the only way forward, so the summary is
// written in parts instead: the transcript is rendered as text, cut into
// pieces that fit, and summarized in order, each request folding in the
// summary so far. Everything here is local to the requests; the persisted
// transcript is never touched, and only the final summary is appended as
// the usual compaction entry.
const (
	// compactPartShare is how much of the estimated room a part may fill.
	// The estimate errs low on code, so a part is sized well under it.
	compactPartShare = 0.6
	// compactMaxParts bounds the summary requests one compaction makes
	// on a first pass; beyond it the oldest content is left out.
	compactMaxParts = 8
	// compactMaxRetries is how many times a part the provider rejected as
	// too large is cut in half and tried again.
	compactMaxRetries = 3
	// compactMinPartTokens is the smallest room worth summarizing in.
	compactMinPartTokens = 256
	// compactOverheadTokens covers a part's instructions and framing.
	compactOverheadTokens = 768
	// compactLatestChars caps the latest request carried verbatim.
	compactLatestChars = 16_000
)

// compactResult is what summarizing produced.
type compactResult struct {
	summary string
	usage   llm.Usage // the request's, when the summary took a single one
	parts   int       // summary requests that produced it
	lost    bool      // content was shortened or left out to fit
}

var errEmptySummary = errors.New("model returned an empty summary")

// summarize writes the summary of msgs, in one request when it fits the
// summarizer's window and in parts when it doesn't (or when the provider
// says it doesn't: the estimate can be wrong). req carries everything but
// the messages; window is the summarizer's context window.
func (a *Agent) summarize(ctx context.Context, emit func(Event), tr *trace.Tracer, provider llm.Provider, req llm.Request, msgs []llm.Message, window int) (compactResult, error) {
	whole := req
	whole.Messages = withUserText(msgs, compactionPrompt)
	size := estimateMessages(whole.Messages)
	room := window - req.MaxTokens - estimatePrefix(req.System, req.Tools)
	// Parts carry no tool definitions, so only the system prompt and the
	// summary's own output allowance come off the window. Where even a part
	// would not fit, the whole request is tried anyway: the provider, not
	// the estimate, has the last word.
	partRoom := window - req.MaxTokens - estimatePrefix(req.System, nil) - compactOverheadTokens
	partsFit := int(compactPartShare*float64(partRoom)) >= 2*compactMinPartTokens
	if window <= 0 || size <= room || !partsFit {
		text, usage, err := a.summaryRequest(ctx, emit, tr, provider, whole)
		switch {
		case err == nil:
			summary := extractSummary(text)
			if summary == "" {
				return compactResult{}, errEmptySummary
			}
			return compactResult{summary: summary, usage: usage, parts: 1}, nil
		case ctx.Err() != nil || !errors.Is(err, llm.ErrContextOverflow):
			return compactResult{}, err
		}
		if window <= 0 { // no window to size parts by: start from half of what failed
			partRoom = size / 2
		}
	}
	emit(Event{Kind: EvNotice, Text: "the conversation is too large to summarize in one request; summarizing it in parts"})
	return a.summarizeInParts(ctx, emit, tr, provider, req, msgs, partRoom)
}

// summarizeInParts is the rolling summary: room is the estimated tokens a
// part request may spend on the summary so far, the latest request and the
// transcript piece together.
func (a *Agent) summarizeInParts(ctx context.Context, emit func(Event), tr *trace.Tracer, provider llm.Provider, req llm.Request, msgs []llm.Message, room int) (compactResult, error) {
	room = int(compactPartShare * float64(room))
	if room < compactMinPartTokens*2 {
		return compactResult{}, fmt.Errorf("the summarizer's context window leaves no room to summarize the conversation in (%d tokens after the system prompt and output allowance)", max(room, 0))
	}
	// The summary so far gets up to a third of the room (it is at most
	// one output allowance long), the latest request an eighth, and the
	// transcript piece the rest.
	carryChars := min(req.MaxTokens, room/3) * charsPerToken
	latestChars := min(compactLatestChars, room/8*charsPerToken)
	pieceChars := room*charsPerToken - carryChars - latestChars

	lost := false
	latest := latestRequest(msgs)
	units := renderTranscript(msgs)
	parts := packUnits(units, pieceChars, &lost)
	if len(parts) > compactMaxParts {
		parts, lost = parts[len(parts)-compactMaxParts:], true
	}
	prev := ""
	retries := 0
	for i := 0; i < len(parts); {
		if err := a.checkBudget(emit); err != nil {
			return compactResult{}, err
		}
		carry, cut := elide(prev, carryChars)
		pinned := ""
		if i == len(parts)-1 {
			pinned, _ = elide(latest, latestChars)
		}
		text, _, err := a.summaryRequest(ctx, emit, tr, provider, partRequest(req, carry, joinUnits(parts[i]), i+1, len(parts), pinned))
		if err != nil {
			if ctx.Err() != nil || !errors.Is(err, llm.ErrContextOverflow) || retries >= compactMaxRetries {
				return compactResult{}, fmt.Errorf("summarizing part %d of %d: %w", i+1, len(parts), err)
			}
			// The estimate was too generous: cut what is left in half and
			// go on, within the same overall number of requests.
			retries++
			carryChars, latestChars, pieceChars = carryChars/2, latestChars/2, pieceChars/2
			var rest []compactUnit
			for _, p := range parts[i:] {
				rest = append(rest, p...)
			}
			again := packUnits(rest, pieceChars, &lost)
			if left := max(compactMaxParts-i, 1); len(again) > left {
				again, lost = again[len(again)-left:], true
			}
			parts = append(parts[:i:i], again...)
			continue
		}
		summary := extractSummary(text)
		if summary == "" {
			return compactResult{}, errEmptySummary
		}
		lost = lost || cut
		prev = summary
		i++
	}
	if latest != "" {
		pinned, cut := elide(latest, latestChars)
		lost = lost || cut
		prev += "\n\nThe latest request in the summarized conversation, verbatim:\n<latest-request>\n" + pinned + "\n</latest-request>"
	}
	return compactResult{summary: prev, parts: len(parts), lost: lost}, nil
}

// partRequest asks for the summary of one piece of the transcript, folded
// into the summary of the pieces before it. It carries no tools: nothing
// in it is a tool call, and the room is better spent on the transcript.
func partRequest(base llm.Request, prev, piece string, part, parts int, latest string) llm.Request {
	var b strings.Builder
	fmt.Fprintf(&b, "This conversation is too long to summarize in one request, so it is being summarized in %d parts, oldest first. This is part %d. The transcript part below is a plain-text rendering of the conversation; tool calls and their results are shown as text.\n\n", parts, part)
	if prev != "" {
		b.WriteString("<summary-so-far>\n" + prev + "\n</summary-so-far>\n\nWrite one summary covering both the summary so far and this part: keep everything in the summary so far that still matters.\n\n")
	}
	b.WriteString("<transcript-part>\n" + piece + "\n</transcript-part>\n\n")
	if latest != "" {
		b.WriteString("<latest-request>\n" + latest + "\n</latest-request>\n\nThis is the latest request in the conversation. The summary must state it and every requirement in it exactly.\n\n")
	}
	b.WriteString(compactionPrompt)
	req := base
	req.Tools = nil
	req.Messages = []llm.Message{llm.UserText(b.String())}
	return req
}

// summaryRequest makes one summary request and returns the reply's text.
// Its spend is recorded on its own: the summary replaces the transcript's
// context rather than joining it.
func (a *Agent) summaryRequest(ctx context.Context, emit func(Event), tr *trace.Tracer, provider llm.Provider, req llm.Request) (string, llm.Usage, error) {
	reqID := tr.Request(provider.Name(), req, "compaction")
	ctx = tr.Wire(ctx, reqID)
	start := time.Now()
	for ev, err := range provider.Stream(ctx, req) {
		if err != nil {
			tr.Response(reqID, trace.Response{Start: start.UnixMilli(), DurationMS: time.Since(start).Milliseconds(), Error: err.Error()})
			return "", llm.Usage{}, err
		}
		switch ev.Type {
		case llm.EventNotice:
			emit(Event{Kind: EvNotice, Text: ev.Text})
		case llm.EventDone:
			final, usage := ev.Message, ev.Usage
			tr.Response(reqID, trace.Response{Message: &final, Usage: &usage, CostUSD: llm.Lookup(req.Model).Cost(usage),
				StopReason: string(ev.StopReason), Start: start.UnixMilli(), DurationMS: time.Since(start).Milliseconds()})
			model := final.Model
			if model == "" {
				model = req.Model
			}
			a.recordUsage(model, usage, emit)
			if a.opts.Session != nil {
				a.saveFailed(a.opts.Session.AppendUsage(model, usage))
			}
			return final.Text(), usage, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return "", llm.Usage{}, err
	}
	return "", llm.Usage{}, nil
}

// compactUnit is one block of the transcript rendered as text.
type compactUnit struct {
	text string
	// start marks the first block of a prompt: no tool call before it is
	// waiting for a result, so a part may begin there cleanly.
	start bool
}

// renderTranscript renders msgs as text for part requests. Thinking and
// provider-opaque blocks are left out (they are never replayed to another
// provider), and media become placeholders. A tool call and its result
// stay in order, labelled with the call's id.
func renderTranscript(msgs []llm.Message) []compactUnit {
	var units []compactUnit
	for _, m := range msgs {
		start := session.IsPrompt(m)
		first := true
		for _, b := range m.Blocks {
			s := renderBlock(b)
			if s == "" {
				continue
			}
			if first {
				s = "[" + string(m.Role) + "]\n" + s
			}
			units = append(units, compactUnit{text: s + "\n\n", start: start && first})
			first = false
		}
	}
	return units
}

func renderBlock(b llm.Block) string {
	switch b.Type {
	case llm.BlockText:
		if b.Attachment != "" {
			return "(attached " + b.Attachment + ")\n" + b.Text
		}
		return b.Text
	case llm.BlockToolUse:
		return fmt.Sprintf("[tool call %s, id %s] %s", b.Name, b.ID, b.Input)
	case llm.BlockToolResult:
		label := "tool result"
		if b.IsError {
			label = "tool error"
		}
		s := fmt.Sprintf("[%s for %s, id %s] %s", label, b.Name, b.ID, b.Content)
		if n := len(b.Images); n > 0 {
			s += fmt.Sprintf(" (%d image(s) not shown)", n)
		}
		return s
	case llm.BlockImage:
		return "[image not shown]"
	case llm.BlockDocument:
		return "[document " + b.Attachment + " not shown]"
	case llm.BlockThinking, llm.BlockOpaque:
		return ""
	}
	return b.Text
}

// packUnits groups units into parts of at most limit characters, keeping a
// prompt and what follows it together when they fit, and shortening any
// single unit larger than a part.
func packUnits(units []compactUnit, limit int, lost *bool) [][]compactUnit {
	var parts [][]compactUnit
	var cur []compactUnit
	size := 0
	flush := func() {
		if len(cur) > 0 {
			parts = append(parts, cur)
			cur, size = nil, 0
		}
	}
	for i := 0; i < len(units); {
		// A prompt and its turn, up to the next prompt.
		j := i + 1
		group := len(units[i].text)
		for j < len(units) && !units[j].start {
			group += len(units[j].text)
			j++
		}
		if size+group > limit && group <= limit {
			flush()
		}
		for _, u := range units[i:j] {
			if t, cut := elide(u.text, limit); cut {
				u.text, *lost = t, true
			}
			if size+len(u.text) > limit {
				flush()
			}
			cur = append(cur, u)
			size += len(u.text)
		}
		i = j
	}
	flush()
	return parts
}

func joinUnits(units []compactUnit) string {
	var b strings.Builder
	for _, u := range units {
		b.WriteString(u.text)
	}
	return strings.TrimSpace(b.String())
}

// latestRequest is the text of the last prompt in msgs, which a summary in
// parts carries verbatim so the task in hand survives whatever else has to
// be shortened. An earlier summary is not a request; it is summarized.
func latestRequest(msgs []llm.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if !session.IsPrompt(msgs[i]) {
			continue
		}
		text := strings.TrimSpace(msgs[i].Text())
		if strings.HasPrefix(text, compactionLead) {
			return ""
		}
		return text
	}
	return ""
}

// compactionLead is how session.CompactionMessage begins.
var compactionLead = strings.TrimSpace(session.CompactionMessage("", "").Text())[:40]

// elide shortens s to at most limit bytes, keeping its beginning and end
// and saying how much was left out. It reports whether it cut anything.
func elide(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	if limit <= 0 {
		return "", s != ""
	}
	marker := fmt.Sprintf("\n[... %d characters left out to fit the summarizer ...]\n", len(s)-limit)
	keep := limit - len(marker)
	if keep <= 0 {
		return headOf(s, limit), true
	}
	head := keep * 2 / 3
	return headOf(s, head) + marker + tailOf(s, keep-head), true
}

// headOf and tailOf cut s to at most n bytes without splitting a rune.
func headOf(s string, n int) string {
	for n > 0 && n < len(s) && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func tailOf(s string, n int) string {
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}
