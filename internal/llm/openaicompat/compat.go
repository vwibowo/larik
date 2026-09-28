// Package openaicompat adapts any OpenAI-compatible Chat Completions endpoint
// (OpenRouter, Ollama, Groq, DeepSeek, xAI, LM Studio, ...) to llm.Provider.
package openaicompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"sort"
	"strings"
	"time"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/shared"

	"larik/internal/llm"
)

// Preset is a well-known endpoint. KeyEnv may be empty for local servers.
type Preset struct {
	BaseURL string
	KeyEnv  string
}

var Presets = map[string]Preset{
	"opencode-free": {"https://opencode.ai/zen/v1", "OPENCODE_API_KEY"},
	"nvidia-nim":    {"https://integrate.api.nvidia.com/v1", "NVIDIA_API_KEY"},
	"openrouter":    {"https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"},
	"groq":          {"https://api.groq.com/openai/v1", "GROQ_API_KEY"},
	"deepseek":      {"https://api.deepseek.com/v1", "DEEPSEEK_API_KEY"},
	"xai":           {"https://api.x.ai/v1", "XAI_API_KEY"},
	"mistral":       {"https://api.mistral.ai/v1", "MISTRAL_API_KEY"},
	"together":      {"https://api.together.xyz/v1", "TOGETHER_API_KEY"},
	"ollama":        {"http://localhost:11434/v1", ""},
	"lmstudio":      {"http://localhost:1234/v1", ""},
}

type Provider struct {
	name   string
	client sdk.Client
	ollama string // native Ollama API base, when talking to Ollama
}

func New(name, apiKey, baseURL string) *Provider {
	if apiKey == "" {
		apiKey = "none" // local servers ignore it, but the SDK requires one
	}
	p := &Provider{name: name, client: sdk.NewClient(option.WithAPIKey(apiKey), option.WithBaseURL(baseURL))}
	if name == "ollama" || strings.Contains(baseURL, ":11434") {
		p.ollama = strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
	}
	return p
}

// ContextWindow asks Ollama for the window a loaded model runs with, which
// defaults to a few thousand tokens regardless of what the model supports.
func (p *Provider) ContextWindow(ctx context.Context, model string) int {
	if p.ollama == "" {
		return 0
	}
	var ps struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if ollamaGet(ctx, p.ollama+"/api/ps", nil, &ps) != nil {
		return 0
	}
	for _, m := range ps.Models {
		if m.Name == model || m.Model == model || m.Name == model+":latest" {
			return m.ContextLength
		}
	}
	return 0
}

// SupportsTools checks Ollama's model capabilities.
func (p *Provider) SupportsTools(ctx context.Context, model string) (bool, bool) {
	if p.ollama == "" {
		return false, false
	}
	var show struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := ollamaGet(ctx, p.ollama+"/api/show", map[string]string{"model": model}, &show); err != nil || show.Capabilities == nil {
		return false, false
	}
	for _, c := range show.Capabilities {
		if c == "tools" {
			return true, true
		}
	}
	return false, true
}

func ollamaGet(ctx context.Context, url string, body any, out any) error {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (p *Provider) Name() string { return p.name }

type pendingCall struct {
	id, name string
	args     strings.Builder
}

func (p *Provider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		params := sdk.ChatCompletionNewParams{Model: shared.ChatModel(req.Model)}
		if req.MaxTokens > 0 {
			params.MaxCompletionTokens = sdk.Int(int64(req.MaxTokens))
		}
		if req.Effort != llm.EffortDefault {
			params.ReasoningEffort = shared.ReasoningEffort(req.Effort)
		}
		opts := []option.RequestOption{
			option.WithJSONSet("messages", p.messages(req)),
			option.WithJSONSet("stream_options", map[string]any{"include_usage": true}),
		}
		if tools := toolsJSON(req.Tools); len(tools) > 0 {
			opts = append(opts, option.WithJSONSet("tools", tools))
		}
		stream := p.client.Chat.Completions.NewStreaming(ctx, params, opts...)
		defer stream.Close()

		var (
			text, reasoning strings.Builder
			calls           = map[int64]*pendingCall{}
			finish          string
			usage           llm.Usage
		)
		for stream.Next() {
			chunk := stream.Current()
			if chunk.JSON.Usage.Valid() && chunk.Usage.PromptTokens > 0 {
				cached := int(chunk.Usage.PromptTokensDetails.CachedTokens)
				usage = llm.Usage{Input: int(chunk.Usage.PromptTokens) - cached, Output: int(chunk.Usage.CompletionTokens), CacheRead: cached}
			}
			for _, choice := range chunk.Choices {
				if choice.FinishReason != "" {
					finish = choice.FinishReason
				}
				d := choice.Delta
				if r := reasoningDelta(d.JSON.ExtraFields); r != "" {
					reasoning.WriteString(r)
					if !yield(llm.StreamEvent{Type: llm.EventThinkingDelta, Text: r}, nil) {
						return
					}
				}
				if d.Content != "" {
					text.WriteString(d.Content)
					if !yield(llm.StreamEvent{Type: llm.EventTextDelta, Text: d.Content}, nil) {
						return
					}
				}
				for _, tc := range d.ToolCalls {
					c, ok := calls[tc.Index]
					if !ok {
						c = &pendingCall{}
						calls[tc.Index] = c
					}
					if tc.ID != "" {
						c.id = tc.ID
					}
					if tc.Function.Name != "" && c.name == "" {
						c.name = tc.Function.Name
						if !yield(llm.StreamEvent{Type: llm.EventToolUseStart, Text: c.name}, nil) {
							return
						}
					}
					c.args.WriteString(tc.Function.Arguments)
				}
			}
		}
		if err := stream.Err(); err != nil {
			yield(llm.StreamEvent{}, convertErr(err))
			return
		}

		msg := llm.Message{Role: llm.RoleAssistant, Model: req.Model}
		if reasoning.Len() > 0 {
			msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockThinking, Text: reasoning.String(), Provider: p.name})
		}
		if text.Len() > 0 {
			msg.Blocks = append(msg.Blocks, llm.TextBlock(text.String()))
		}
		idx := make([]int64, 0, len(calls))
		for i := range calls {
			idx = append(idx, i)
		}
		sort.Slice(idx, func(a, b int) bool { return idx[a] < idx[b] })
		for n, i := range idx {
			c := calls[i]
			if c.id == "" {
				c.id = fmt.Sprintf("call_%d", n)
			}
			input := json.RawMessage(c.args.String())
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			} else if !json.Valid(input) {
				input = nil
			}
			msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockToolUse, ID: c.id, Name: c.name, Input: input})
		}

		stop := llm.StopEnd
		switch {
		case len(calls) > 0:
			stop = llm.StopToolUse
		case finish == "length":
			stop = llm.StopMaxTokens
		case finish == "content_filter":
			stop = llm.StopRefusal
		}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: stop, Usage: usage}, nil)
	}
}

// reasoningDelta reads the non-standard reasoning fields used by DeepSeek,
// OpenRouter, Ollama and others.
func reasoningDelta(extra map[string]respjson.Field) string {
	for _, key := range []string{"reasoning_content", "reasoning"} {
		f, ok := extra[key]
		if !ok {
			continue
		}
		var s string
		if json.Unmarshal([]byte(f.Raw()), &s) == nil && s != "" {
			return s
		}
	}
	return ""
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

func (p *Provider) messages(req llm.Request) []map[string]any {
	var out []map[string]any
	if req.System != "" {
		out = append(out, map[string]any{"role": "system", "content": req.System})
	}
	for _, m := range req.Messages {
		blocks := llm.ReplayableBlocks(m, p.name, req.Model)
		if m.Role == llm.RoleUser {
			var parts []map[string]any
			for _, b := range blocks {
				switch b.Type {
				case llm.BlockToolResult:
					out = append(out, map[string]any{"role": "tool", "tool_call_id": b.ID, "content": b.Content})
				case llm.BlockText:
					parts = append(parts, map[string]any{"type": "text", "text": b.Text})
				case llm.BlockImage:
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + b.MediaType + ";base64," + b.Data}})
				}
			}
			if len(parts) == 1 && parts[0]["type"] == "text" {
				out = append(out, map[string]any{"role": "user", "content": parts[0]["text"]})
			} else if len(parts) > 0 {
				out = append(out, map[string]any{"role": "user", "content": parts})
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
				msg["reasoning_content"] = b.Text
			case llm.BlockToolUse:
				args := string(b.Input)
				if args == "" {
					args = "{}"
				}
				calls = append(calls, map[string]any{"id": b.ID, "type": "function", "function": map[string]any{"name": b.Name, "arguments": args}})
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

func convertErr(err error) error {
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		body := strings.ToLower(apiErr.Error())
		if apiErr.Code == "context_length_exceeded" || strings.Contains(body, "context length") || strings.Contains(body, "maximum context") {
			return fmt.Errorf("%w: %v", llm.ErrContextOverflow, err)
		}
		return llm.ClassifyStatus(apiErr.StatusCode, err)
	}
	return err
}
