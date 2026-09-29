// Package anthropic adapts the Claude Messages API to llm.Provider.
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"larik/internal/llm"
)

const Name = "anthropic"

// DefaultModel is used when the user has an Anthropic key and picks no model.
const DefaultModel = "claude-opus-5"

type Provider struct {
	client sdk.Client
	name   string
}

// New builds a provider. name is the configured provider name ("anthropic"
// or a custom provider of this type); thinking blocks are replayed only to
// the same name. An empty apiKey lets the SDK resolve credentials itself
// (ANTHROPIC_API_KEY, ANTHROPIC_AUTH_TOKEN, `ant auth login` profiles).
func New(name, apiKey, baseURL string) *Provider {
	if name == "" {
		name = Name
	}
	opts := []option.RequestOption{option.WithHTTPClient(llm.HTTPClient)}
	if apiKey != "" {
		opts = append(opts, option.WithAPIKey(apiKey))
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Provider{client: sdk.NewClient(opts...), name: name}
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		started, err := p.attempt(ctx, req, false, yield)
		// Thinking blocks are bound to the exact prefix that produced them
		// (system, tools, earlier messages). If that changed — e.g. a resumed
		// session with different MCP tools — the API rejects them; the
		// documented recovery is to strip thinking and retry once.
		if err != nil && !started && thinkingMismatch(err) && hasThinking(req, p.name) {
			started, err = p.attempt(ctx, req, true, yield)
		}
		if err != nil && err != errStopped {
			yield(llm.StreamEvent{}, err)
		}
	}
}

// errStopped signals that the consumer stopped iterating.
var errStopped = errors.New("stopped")

// attempt streams one request, yielding events but returning (not yielding)
// errors. started reports whether any event reached the consumer.
func (p *Provider) attempt(ctx context.Context, req llm.Request, stripThinking bool, yield func(llm.StreamEvent, error) bool) (started bool, _ error) {
	params, err := buildParams(req, p.name, stripThinking)
	if err != nil {
		return false, err
	}
	stream := p.client.Messages.NewStreaming(ctx, params)
	defer stream.Close()

	var msg sdk.Message
	// Eager input streaming skips server-side validation, so a truncated
	// tool input can arrive; remember which blocks failed to parse.
	invalidInput := map[int64]bool{}
	stopped := map[int64]bool{}
	for stream.Next() {
		ev := stream.Current()
		if ev.Type == "content_block_stop" && int(ev.Index) < len(msg.Content) {
			stopped[ev.Index] = true
			cb := msg.Content[ev.Index]
			if cb.Type == "tool_use" && len(cb.Input) > 0 && !json.Valid(cb.Input) {
				invalidInput[ev.Index] = true
			}
		}
		if err := msg.Accumulate(ev); err != nil {
			return started, err
		}
		var out llm.StreamEvent
		switch ev.Type {
		case "content_block_start":
			if ev.ContentBlock.Type != "tool_use" {
				continue
			}
			out = llm.StreamEvent{Type: llm.EventToolUseStart, Text: ev.ContentBlock.Name}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				out = llm.StreamEvent{Type: llm.EventTextDelta, Text: ev.Delta.Text}
			case "thinking_delta":
				out = llm.StreamEvent{Type: llm.EventThinkingDelta, Text: ev.Delta.Thinking}
			default:
				continue
			}
		default:
			continue
		}
		started = true
		if !yield(out, nil) {
			return true, errStopped
		}
	}
	if err := stream.Err(); err != nil {
		return started, convertErr(err)
	}
	if msg.StopReason == "model_context_window_exceeded" {
		return started, llm.ErrContextOverflow
	}
	// A tool call cut off before its block ended (the output limit) has
	// incomplete input, even when what arrived happens to parse.
	for i, cb := range msg.Content {
		if cb.Type == "tool_use" && !stopped[int64(i)] {
			invalidInput[int64(i)] = true
		}
	}
	yield(llm.StreamEvent{
		Type:       llm.EventDone,
		Message:    convertMessage(msg, req.Model, p.name, invalidInput),
		StopReason: convertStop(msg.StopReason),
		Usage: llm.Usage{
			Input:      int(msg.Usage.InputTokens),
			Output:     int(msg.Usage.OutputTokens),
			CacheRead:  int(msg.Usage.CacheReadInputTokens),
			CacheWrite: int(msg.Usage.CacheCreationInputTokens),
		},
	}, nil)
	return true, nil
}

func thinkingMismatch(err error) bool {
	var apiErr *llm.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 400 {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "thinking") || strings.Contains(msg, "prefix_binding")
}

func hasThinking(req llm.Request, name string) bool {
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockThinking && b.Provider == name {
				return true
			}
		}
	}
	return false
}

func buildParams(req llm.Request, name string, stripThinking bool) (sdk.MessageNewParams, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 64_000
	}
	params := sdk.MessageNewParams{
		Model:     sdk.Model(req.Model),
		MaxTokens: int64(maxTokens),
		// Auto-places a breakpoint on the last cacheable block each turn.
		CacheControl: sdk.NewCacheControlEphemeralParam(),
	}
	if req.System != "" {
		params.System = []sdk.TextBlockParam{{Text: req.System, CacheControl: sdk.NewCacheControlEphemeralParam()}}
	}

	if legacyThinking(req.Model) {
		// Haiku 4.5 and older take a fixed budget and reject effort.
		if req.Effort != llm.EffortDefault {
			budget := map[llm.Effort]int64{llm.EffortLow: 2048, llm.EffortMedium: 8192}[req.Effort]
			if budget == 0 {
				budget = 16_384
			}
			if budget < int64(maxTokens) {
				params.Thinking = sdk.ThinkingConfigParamOfEnabled(budget)
			}
		}
	} else {
		adaptive := sdk.ThinkingConfigAdaptiveParam{Display: sdk.ThinkingConfigAdaptiveDisplaySummarized}
		params.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &adaptive}
		if req.Effort != llm.EffortDefault {
			params.OutputConfig = sdk.OutputConfigParam{Effort: sdk.OutputConfigEffort(req.Effort)}
		}
	}

	for _, t := range req.Tools {
		schema, err := toolSchema(t.Schema)
		if err != nil {
			return params, fmt.Errorf("tool %s: %w", t.Name, err)
		}
		params.Tools = append(params.Tools, sdk.ToolUnionParam{OfTool: &sdk.ToolParam{
			Name:                t.Name,
			Description:         sdk.String(t.Description),
			InputSchema:         schema,
			EagerInputStreaming: sdk.Bool(true),
		}})
	}

	for _, m := range req.Messages {
		var blocks []sdk.ContentBlockParamUnion
		for _, b := range llm.ReplayableBlocks(m, name, req.Model) {
			if stripThinking && b.Type == llm.BlockThinking {
				continue
			}
			if cb, ok := toParam(b); ok {
				blocks = append(blocks, cb)
			}
		}
		if len(blocks) == 0 {
			continue
		}
		if m.Role == llm.RoleAssistant {
			params.Messages = append(params.Messages, sdk.NewAssistantMessage(blocks...))
		} else {
			params.Messages = append(params.Messages, sdk.NewUserMessage(blocks...))
		}
	}
	return params, nil
}

// legacyThinking reports models that predate adaptive thinking.
func legacyThinking(model string) bool {
	for _, v := range []string{"claude-3", "-4-0", "-4-1", "-4-5", "sonnet-4-2", "opus-4-2"} {
		if strings.Contains(model, v) {
			return true
		}
	}
	return false
}

func toParam(b llm.Block) (sdk.ContentBlockParamUnion, bool) {
	switch b.Type {
	case llm.BlockText:
		if b.Text == "" {
			return sdk.ContentBlockParamUnion{}, false
		}
		return sdk.NewTextBlock(b.Text), true
	case llm.BlockThinking:
		if b.Redacted {
			return sdk.NewRedactedThinkingBlock(b.Signature), true
		}
		return sdk.NewThinkingBlock(b.Signature, b.Text), true
	case llm.BlockToolUse:
		input := b.Input
		if len(input) == 0 || !json.Valid(input) {
			input = json.RawMessage(`{}`)
		}
		return sdk.NewToolUseBlock(b.ID, input, b.Name), true
	case llm.BlockToolResult:
		content := b.Content
		if content == "" {
			content = "(no output)"
		}
		res := sdk.NewToolResultBlock(b.ID, content, b.IsError)
		for _, img := range b.Images {
			res.OfToolResult.Content = append(res.OfToolResult.Content, sdk.ToolResultBlockParamContentUnion{
				OfImage: sdk.NewImageBlockBase64(img.MediaType, img.Data).OfImage,
			})
		}
		return res, true
	case llm.BlockImage:
		return sdk.NewImageBlockBase64(b.MediaType, b.Data), true
	}
	return sdk.ContentBlockParamUnion{}, false
}

func toolSchema(raw json.RawMessage) (sdk.ToolInputSchemaParam, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return sdk.ToolInputSchemaParam{}, err
	}
	s := sdk.ToolInputSchemaParam{Properties: m["properties"]}
	if req, ok := m["required"].([]any); ok {
		for _, r := range req {
			if name, ok := r.(string); ok {
				s.Required = append(s.Required, name)
			}
		}
	}
	delete(m, "type")
	delete(m, "properties")
	delete(m, "required")
	if len(m) > 0 {
		s.ExtraFields = m
	}
	return s, nil
}

func convertMessage(msg sdk.Message, model, name string, invalidInput map[int64]bool) llm.Message {
	out := llm.Message{Role: llm.RoleAssistant, Model: model}
	for i, cb := range msg.Content {
		switch cb.Type {
		case "text":
			out.Blocks = append(out.Blocks, llm.TextBlock(cb.Text))
		case "thinking":
			out.Blocks = append(out.Blocks, llm.Block{Type: llm.BlockThinking, Text: cb.Thinking, Signature: cb.Signature, Provider: name})
		case "redacted_thinking":
			out.Blocks = append(out.Blocks, llm.Block{Type: llm.BlockThinking, Redacted: true, Signature: cb.Data, Provider: name})
		case "tool_use":
			b := llm.Block{Type: llm.BlockToolUse, ID: cb.ID, Name: cb.Name, Input: cb.Input}
			if invalidInput[int64(i)] {
				// Signal the agent to return an INVALID_JSON error result.
				b.Input = nil
			}
			out.Blocks = append(out.Blocks, b)
		}
	}
	return out
}

func convertStop(r sdk.StopReason) llm.StopReason {
	switch r {
	case "end_turn", "stop_sequence":
		return llm.StopEnd
	case "tool_use":
		return llm.StopToolUse
	case "max_tokens":
		return llm.StopMaxTokens
	case "refusal":
		return llm.StopRefusal
	}
	return llm.StopOther
}

func convertErr(err error) error {
	var apiErr *sdk.Error
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 400 && strings.Contains(strings.ToLower(apiErr.Error()), "prompt is too long") {
			return fmt.Errorf("%w: %v", llm.ErrContextOverflow, err)
		}
		return llm.ClassifyStatus(apiErr.StatusCode, err)
	}
	return err
}
