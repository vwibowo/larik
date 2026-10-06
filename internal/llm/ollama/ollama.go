// Package ollama adapts Ollama's native chat API (/api/chat) to
// llm.Provider.
//
// Ollama's OpenAI-compatible endpoint ignores the context size a request
// asks for and loads models with the server default, 4096 tokens unless
// OLLAMA_CONTEXT_LENGTH is set, which Larik's system prompt and tool
// definitions nearly fill. The native API takes num_ctx per request, so
// this adapter can load models with a usable window.
package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"strings"
	"sync"
	"time"

	"larik/internal/llm"
)

const Name = "ollama"

// DefaultContextLength is the window requested when none is configured,
// capped at what the model supports.
const DefaultContextLength = 32768

type Provider struct {
	name   string
	base   string // native API root, e.g. http://localhost:11434
	numCtx int    // configured window; 0 means DefaultContextLength

	mu     sync.Mutex
	maxCtx map[string]int // model → the largest window it supports, from /api/show
}

// New returns a provider for the Ollama server at baseURL, which may be the
// OpenAI-compatible URL (…/v1). contextLength sets num_ctx; 0 uses
// DefaultContextLength capped at the model's maximum.
func New(name, baseURL string, contextLength int) *Provider {
	return &Provider{
		name:   name,
		base:   strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1"),
		numCtx: contextLength,
		maxCtx: map[string]int{},
	}
}

func (p *Provider) Name() string { return p.name }

// contextLength is the num_ctx to request for model.
func (p *Provider) contextLength(ctx context.Context, model string) int {
	if p.numCtx > 0 {
		return p.numCtx
	}
	if limit := p.modelMax(ctx, model); limit > 0 && limit < DefaultContextLength {
		return limit
	}
	return DefaultContextLength
}

// modelMax asks /api/show for the model's trained context length, once.
func (p *Provider) modelMax(ctx context.Context, model string) int {
	p.mu.Lock()
	n, ok := p.maxCtx[model]
	p.mu.Unlock()
	if ok {
		return n
	}
	var show struct {
		ModelInfo map[string]any `json:"model_info"`
	}
	if call(ctx, p.base+"/api/show", map[string]string{"model": model}, &show) == nil {
		for k, v := range show.ModelInfo {
			if f, ok := v.(float64); ok && strings.HasSuffix(k, ".context_length") {
				n = int(f)
			}
		}
	}
	p.mu.Lock()
	p.maxCtx[model] = n
	p.mu.Unlock()
	return n
}

// ContextWindow reports the window the loaded model actually runs with.
func (p *Provider) ContextWindow(ctx context.Context, model string) int {
	var ps struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if call(ctx, p.base+"/api/ps", nil, &ps) != nil {
		return 0
	}
	for _, m := range ps.Models {
		if m.Name == model || m.Model == model || m.Name == model+":latest" {
			return m.ContextLength
		}
	}
	return 0
}

// SupportsTools checks the model's capabilities.
func (p *Provider) SupportsTools(ctx context.Context, model string) (bool, bool) {
	var show struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := call(ctx, p.base+"/api/show", map[string]string{"model": model}, &show); err != nil || show.Capabilities == nil {
		return false, false
	}
	for _, c := range show.Capabilities {
		if c == "tools" {
			return true, true
		}
	}
	return false, true
}

// chunk is one line of the /api/chat stream.
type chunk struct {
	Message struct {
		Content   string     `json:"content"`
		Thinking  string     `json:"thinking"`
		ToolCalls []toolCall `json:"tool_calls"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	Error           string `json:"error"`
}

type toolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

func (p *Provider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		options := map[string]any{"num_ctx": p.contextLength(ctx, req.Model)}
		if req.MaxTokens > 0 {
			options["num_predict"] = req.MaxTokens
		}
		body := map[string]any{
			"model":    req.Model,
			"messages": p.messages(req),
			"stream":   true,
			"options":  options,
		}
		if tools := toolsJSON(req.Tools); len(tools) > 0 {
			body["tools"] = tools
		}
		if think := thinkLevel(req.Effort); think != "" {
			body["think"] = think
		}
		b, _ := json.Marshal(body)
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/api/chat", bytes.NewReader(b))
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		hreq.Header.Set("Content-Type", "application/json")
		resp, err := llm.HTTPClient.Do(hreq)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			yield(llm.StreamEvent{}, statusErr(resp))
			return
		}

		var (
			text, thinking strings.Builder
			calls          []toolCall
			last           chunk
		)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var c chunk
			if err := json.Unmarshal(line, &c); err != nil {
				yield(llm.StreamEvent{}, fmt.Errorf("ollama: bad stream line: %w", err))
				return
			}
			if c.Error != "" {
				yield(llm.StreamEvent{}, errors.New("ollama: "+c.Error))
				return
			}
			if t := c.Message.Thinking; t != "" {
				thinking.WriteString(t)
				if !yield(llm.StreamEvent{Type: llm.EventThinkingDelta, Text: t}, nil) {
					return
				}
			}
			if t := c.Message.Content; t != "" {
				text.WriteString(t)
				if !yield(llm.StreamEvent{Type: llm.EventTextDelta, Text: t}, nil) {
					return
				}
			}
			for _, tc := range c.Message.ToolCalls { // Ollama sends each call whole
				calls = append(calls, tc)
				if !yield(llm.StreamEvent{Type: llm.EventToolUseStart, Text: tc.Function.Name}, nil) {
					return
				}
			}
			if c.Done {
				last = c
				break
			}
		}
		if err := sc.Err(); err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		if !last.Done {
			yield(llm.StreamEvent{}, errors.New("ollama: stream ended before the response was done"))
			return
		}

		msg := llm.Message{Role: llm.RoleAssistant, Model: req.Model}
		if thinking.Len() > 0 {
			msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockThinking, Text: thinking.String(), Provider: p.name})
		}
		if text.Len() > 0 {
			msg.Blocks = append(msg.Blocks, llm.TextBlock(text.String()))
		}
		for _, tc := range calls {
			id := tc.ID
			if id == "" {
				id = llm.NewCallID("call_")
			}
			input := tc.Function.Arguments
			if len(input) == 0 || string(input) == "null" {
				input = json.RawMessage(`{}`)
			}
			msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockToolUse, ID: id, Name: tc.Function.Name, Input: input})
		}
		stop := llm.StopEnd
		switch {
		case len(calls) > 0:
			stop = llm.StopToolUse
		case last.DoneReason == "length":
			stop = llm.StopMaxTokens
		}
		usage := llm.Usage{Input: last.PromptEvalCount, Output: last.EvalCount}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: stop, Usage: usage}, nil)
	}
}

// thinkLevel maps effort to Ollama's think parameter; "" leaves the
// model's default.
func thinkLevel(e llm.Effort) string {
	switch e {
	case llm.EffortDefault:
		return ""
	case llm.EffortLow, llm.EffortMedium, llm.EffortHigh:
		return string(e)
	}
	return string(llm.EffortHigh) // xhigh, max: Ollama's top level
}

func toolsJSON(specs []llm.ToolSpec) []map[string]any {
	var tools []map[string]any
	for _, t := range specs {
		tools = append(tools, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"parameters":  t.Schema,
			},
		})
	}
	return tools
}

// messages converts the conversation. Tool results name their tool, which
// Ollama matches to the call; thinking is replayed only to the model that
// produced it.
func (p *Provider) messages(req llm.Request) []map[string]any {
	var out []map[string]any
	if req.System != "" {
		out = append(out, map[string]any{"role": "system", "content": req.System})
	}
	toolNames := map[string]string{} // call id → tool name
	for _, m := range llm.RepairToolHistory(req.Messages) {
		blocks := llm.ReplayableBlocks(m, p.name, req.Model)
		if m.Role == llm.RoleUser {
			var text []string
			var images []string
			for _, b := range blocks {
				switch b.Type {
				case llm.BlockToolResult:
					out = append(out, map[string]any{"role": "tool", "tool_name": toolNames[b.ID], "content": b.Content})
					if len(b.Images) > 0 {
						text = append(text, llm.ToolImagesNote(b))
						for _, img := range b.Images {
							images = append(images, img.Data)
						}
					}
				case llm.BlockText:
					text = append(text, b.Text)
				case llm.BlockImage:
					images = append(images, b.Data)
				}
			}
			if len(text) > 0 || len(images) > 0 {
				msg := map[string]any{"role": "user", "content": strings.Join(text, "\n\n")}
				if len(images) > 0 {
					msg["images"] = images
				}
				out = append(out, msg)
			}
			continue
		}
		msg := map[string]any{"role": "assistant"}
		var text strings.Builder
		var calls []map[string]any
		for _, b := range blocks {
			switch b.Type {
			case llm.BlockText:
				text.WriteString(b.Text)
			case llm.BlockThinking:
				msg["thinking"] = b.Text
			case llm.BlockToolUse:
				toolNames[b.ID] = b.Name
				args := b.Input
				if !json.Valid(args) {
					args = json.RawMessage(`{}`)
				}
				calls = append(calls, map[string]any{"function": map[string]any{"name": b.Name, "arguments": args}})
			}
		}
		msg["content"] = text.String()
		if len(calls) > 0 {
			msg["tool_calls"] = calls
		}
		out = append(out, msg)
	}
	return out
}

// statusErr turns a failed response into an error, keeping Ollama's own
// message ("model 'x' not found").
func statusErr(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e struct {
		Error string `json:"error"`
	}
	msg := strings.TrimSpace(string(data))
	if json.Unmarshal(data, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	err := llm.ClassifyStatus(resp.StatusCode, fmt.Errorf("ollama: %s (HTTP %d)", msg, resp.StatusCode))
	err.(*llm.APIError).RetryAfter = llm.ParseRetryAfter(resp.Header.Get("Retry-After"))
	return err
}

// call does a small JSON request: POST with body, or GET without.
func call(ctx context.Context, url string, body, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	method, reader := http.MethodGet, io.Reader(nil)
	if body != nil {
		b, _ := json.Marshal(body)
		method, reader = http.MethodPost, bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	resp, err := llm.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
