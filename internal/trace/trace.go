// Package trace records what an agent does, for debug mode: each model
// request as sent (and its raw HTTP exchange), each response with its
// timing, tool calls, permission answers, hooks and notices. Records go
// to <session>.trace/events.jsonl, read by `larik trace`.
package trace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"larik/internal/llm"
)

// EventsFile is the record log inside a trace directory.
const EventsFile = "events.jsonl"

// Record is one line of the log. Fields beyond the common ones depend on
// Kind; see the Kind constants.
type Record struct {
	Seq    int64  `json:"seq"`
	T      int64  `json:"t"` // unix milliseconds
	Kind   string `json:"kind"`
	Agent  string `json:"agent,omitempty"`  // a subagent's label; empty for the main agent
	Parent string `json:"parent,omitempty"` // for a subagent, the task call that started it
	Req    string `json:"req,omitempty"`    // the model request a record belongs to

	Data any `json:"data,omitempty"`
}

// Record kinds.
const (
	KindTurnStart  = "turn_start"
	KindTurnEnd    = "turn_end"
	KindRequest    = "request"
	KindResponse   = "response"
	KindHTTP       = "http"
	KindToolStart  = "tool_start"
	KindToolEnd    = "tool_end"
	KindPermission = "permission"
	KindHook       = "hook"
	KindCompaction = "compaction"
	KindNotice     = "notice"
	KindError      = "error"
	KindState      = "state" // recording started or stopped
)

// Recorder owns a trace directory. It is shared by a session's agents,
// each writing through its own Tracer.
type Recorder struct {
	dir string

	mu    sync.Mutex
	f     *os.File
	seq   int64
	nreq  int
	nhttp int
	err   error
}

// Open opens (or continues) the trace in dir.
func Open(dir string) (*Recorder, error) {
	if err := os.MkdirAll(filepath.Join(dir, "http"), 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, EventsFile)
	r := &Recorder{dir: dir}
	// Continue numbering after an earlier run of the same session.
	if data, err := os.ReadFile(path); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			var rec Record
			if json.Unmarshal([]byte(line), &rec) == nil && rec.Seq > r.seq {
				r.seq = rec.Seq
			}
		}
		entries, _ := os.ReadDir(filepath.Join(dir, "http"))
		r.nhttp = len(entries)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	r.f = f
	r.nreq = int(r.seq) // request ids only need to be unique
	return r, nil
}

// Dir is the trace directory.
func (r *Recorder) Dir() string { return r.dir }

// Size is the trace's size on disk, bodies included.
func (r *Recorder) Size() int64 {
	var n int64
	filepath.WalkDir(r.dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

// Close closes the log.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	err := r.f.Close()
	r.f = nil
	return err
}

// Err is the first write failure, if any; recording stops at it.
func (r *Recorder) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *Recorder) write(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil || r.err != nil {
		return
	}
	r.seq++
	rec.Seq = r.seq
	if rec.T == 0 {
		rec.T = time.Now().UnixMilli()
	}
	data, err := json.Marshal(rec)
	if err == nil {
		_, err = r.f.Write(append(data, '\n'))
	}
	if err != nil {
		r.err = err
	}
}

func (r *Recorder) nextReq() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nreq++
	return fmt.Sprintf("r%d", r.nreq)
}

// writeBody stores a body file and returns its name.
func (r *Recorder) writeBody(n int, suffix string, data []byte) string {
	if len(data) == 0 {
		return ""
	}
	name := fmt.Sprintf("%04d-%s", n, suffix)
	if err := os.WriteFile(filepath.Join(r.dir, "http", name), data, 0o600); err != nil {
		return ""
	}
	return name
}

// Tracer records one agent's activity. A nil *Tracer records nothing, so
// callers needn't check whether debug mode is on.
type Tracer struct {
	rec    *Recorder
	agent  string
	parent string

	mu        sync.Mutex
	sent      int    // messages already recorded for this agent's context
	sentHash  string // hash of those messages, to notice a rewritten context
	system    string // hash of the last system prompt recorded
	toolsHash string
}

// Tracer returns the main agent's tracer.
func (r *Recorder) Tracer() *Tracer {
	if r == nil {
		return nil
	}
	return &Tracer{rec: r}
}

// Child returns a tracer for a subagent labeled agent, started by the tool
// call parent.
func (t *Tracer) Child(agent, parent string) *Tracer {
	if t == nil {
		return nil
	}
	return &Tracer{rec: t.rec, agent: agent, parent: parent}
}

// Recorder is the recorder behind t.
func (t *Tracer) Recorder() *Recorder {
	if t == nil {
		return nil
	}
	return t.rec
}

func (t *Tracer) write(kind, req string, data any) {
	t.rec.write(Record{Kind: kind, Agent: t.agent, Parent: t.parent, Req: req, Data: data})
}

// TurnStart records a prompt: as typed, and as sent when a skill, a
// command or hook context changed it.
func (t *Tracer) TurnStart(typed, sent string, attachments []string) {
	if t == nil {
		return
	}
	d := map[string]any{"prompt": typed}
	if sent != typed {
		d["sent"] = sent
	}
	if len(attachments) > 0 {
		d["attachments"] = attachments
	}
	t.write(KindTurnStart, "", d)
}

// TurnEnd records why a turn ended.
func (t *Tracer) TurnEnd(stop string) {
	if t == nil {
		return
	}
	t.write(KindTurnEnd, "", map[string]any{"stop": stop})
}

// Request records a model request and returns its id. purpose names a
// request that isn't a turn of the conversation, such as "compaction". The system prompt
// and tools are written when they change; messages only from where the
// last request's ended, unless the context was rewritten (compaction,
// /clear), when "reset" is set and all of them are written.
func (t *Tracer) Request(provider string, req llm.Request, purpose string) string {
	if t == nil {
		return ""
	}
	id := t.rec.nextReq()
	d := map[string]any{
		"provider": provider, "model": req.Model, "max_tokens": req.MaxTokens,
		"messages_total": len(req.Messages),
	}
	if req.Effort != "" {
		d["effort"] = req.Effort
	}
	if purpose != "" {
		d["purpose"] = purpose
	}
	t.mu.Lock()
	sys := hashOf(req.System)
	d["system_hash"] = sys
	if sys != t.system {
		d["system"], t.system = req.System, sys
	}
	toolsJSON, _ := json.Marshal(req.Tools)
	th := hashOf(string(toolsJSON))
	d["tools_hash"] = th
	if th != t.toolsHash {
		d["tools"], t.toolsHash = req.Tools, th
	} else {
		names := make([]string, len(req.Tools))
		for i, s := range req.Tools {
			names[i] = s.Name
		}
		d["tool_names"] = names
	}
	from := t.sent
	if from > len(req.Messages) || from > 0 && messagesHash(req.Messages[:from]) != t.sentHash {
		from = 0
		d["reset"] = true
	}
	d["from"] = from
	d["messages"] = req.Messages[from:]
	t.sent, t.sentHash = len(req.Messages), messagesHash(req.Messages)
	t.mu.Unlock()
	t.write(KindRequest, id, d)
	return id
}

// Response is what came back for a request.
type Response struct {
	Message    *llm.Message `json:"message,omitempty"`
	Usage      *llm.Usage   `json:"usage,omitempty"`
	CostUSD    float64      `json:"cost_usd,omitempty"`
	StopReason string       `json:"stop_reason,omitempty"`
	TTFTMS     int64        `json:"ttft_ms"`               // until the first streamed output of any kind
	ThinkingMS int64        `json:"thinking_ms,omitempty"` // until the first text or tool call
	DurationMS int64        `json:"duration_ms"`
	Start      int64        `json:"start"` // unix ms the request was sent
	Error      string       `json:"error,omitempty"`
	Notices    []string     `json:"notices,omitempty"` // retries, fallbacks
}

// Response records the outcome of request req.
func (t *Tracer) Response(req string, r Response) {
	if t == nil {
		return
	}
	t.write(KindResponse, req, r)
}

// RecordHTTP implements llm.WireRecorder.
func (t *Tracer) RecordHTTP(x *llm.WireExchange) {
	if t == nil {
		return
	}
	t.rec.mu.Lock()
	t.rec.nhttp++
	n := t.rec.nhttp
	t.rec.mu.Unlock()
	d := map[string]any{
		"method": x.Method, "url": x.URL, "status": x.Status,
		"request_headers": x.ReqHeader, "response_headers": x.RespHeader,
		"sent": x.Sent.UnixMilli(), "headers": x.Headers.UnixMilli(), "done": x.Done.UnixMilli(),
		"request_bytes": len(x.ReqBody), "response_bytes": len(x.RespBody),
	}
	if name := t.rec.writeBody(n, "request.json", x.ReqBody); name != "" {
		d["request_body"] = name
	}
	if name := t.rec.writeBody(n, "response.txt", x.RespBody); name != "" {
		d["response_body"] = name
	}
	if x.Truncated {
		d["truncated"] = true
	}
	if x.Err != "" {
		d["error"] = x.Err
	}
	t.write(KindHTTP, x.Req, d)
}

// Wire tags ctx so request req's HTTP exchange is recorded.
func (t *Tracer) Wire(ctx context.Context, req string) context.Context {
	if t == nil {
		return ctx
	}
	return llm.WithWire(ctx, t, req)
}

// ToolStart and ToolEnd record a tool call.
func (t *Tracer) ToolStart(id, name string, input json.RawMessage) {
	if t == nil {
		return
	}
	t.write(KindToolStart, "", map[string]any{"id": id, "name": name, "input": input})
}

func (t *Tracer) ToolEnd(id, name, output string, isError bool) {
	if t == nil {
		return
	}
	t.write(KindToolEnd, "", map[string]any{"id": id, "name": name, "output": output, "is_error": isError})
}

// Permission records a permission prompt's answer and how long it took.
func (t *Tracer) Permission(id, tool, rule, decision, reason string, asked time.Time) {
	if t == nil {
		return
	}
	d := map[string]any{"id": id, "tool": tool, "rule": rule, "decision": decision,
		"asked": asked.UnixMilli(), "wait_ms": time.Since(asked).Milliseconds()}
	if reason != "" {
		d["reason"] = reason
	}
	t.write(KindPermission, "", d)
}

// Hook records a hook event's run.
func (t *Tracer) Hook(event string, start time.Time, data map[string]any) {
	if t == nil {
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	data["event"], data["start"], data["duration_ms"] = event, start.UnixMilli(), time.Since(start).Milliseconds()
	t.write(KindHook, "", data)
}

// Note records a notice, an error or a compaction.
func (t *Tracer) Note(kind, text string) {
	if t == nil {
		return
	}
	t.write(kind, "", map[string]any{"text": text})
}

// State records recording starting or stopping.
func (t *Tracer) State(on bool) {
	if t == nil {
		return
	}
	t.write(KindState, "", map[string]any{"recording": on})
}

func hashOf(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:6])
}

func messagesHash(msgs []llm.Message) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, m := range msgs {
		enc.Encode(m)
	}
	return hex.EncodeToString(h.Sum(nil)[:6])
}
