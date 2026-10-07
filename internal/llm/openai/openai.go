// Package openai adapts the OpenAI Responses API to llm.Provider.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"path/filepath"
	"strings"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"larik/internal/llm"
	"larik/internal/llm/openaisdk"
)

const Name = "openai"

const DefaultModel = "gpt-5.5"

type Provider struct {
	client sdk.Client
	name   string
	// chatGPT is the backend behind a ChatGPT sign-in: it doesn't accept
	// max_output_tokens, and takes the cache key from session headers.
	chatGPT bool
}

// New builds a Responses API provider. name is the configured provider
// name ("openai" or a custom provider of this type); reasoning items are
// replayed only to the same name, since another organization can't
// decrypt them.
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

// TokenFunc returns a bearer token and ChatGPT account id per request.
type TokenFunc func(ctx context.Context) (token, account string, err error)

// NewChatGPT talks to the Responses API behind a ChatGPT sign-in (Codex
// models on the user's plan). token is called for every request, so it
// can refresh the sign-in.
func NewChatGPT(name, baseURL string, token TokenFunc) *Provider {
	auth := func(r *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		tok, account, err := token(r.Context())
		if err != nil {
			return nil, err
		}
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("chatgpt-account-id", account)
		r.Header.Set("originator", "larik")
		r.Header.Set("OpenAI-Beta", "responses=experimental")
		return next(r)
	}
	client := sdk.NewClient(
		option.WithAPIKey("chatgpt-sign-in"), // replaced per request
		option.WithBaseURL(baseURL),
		option.WithMiddleware(auth),
		option.WithHTTPClient(llm.HTTPClient),
	)
	return &Provider{client: client, name: name, chatGPT: true}
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		params, opts := p.buildParams(req)
		stream := p.client.Responses.NewStreaming(ctx, params, opts...)
		defer stream.Close()

		var final *responses.Response
		// Finished output items as they stream. The ChatGPT backend sends
		// its response.completed with an empty output list, so these are
		// the only copy of the answer there.
		var done []responses.ResponseOutputItemUnion
		for stream.Next() {
			ev := stream.Current()
			var out llm.StreamEvent
			switch ev.Type {
			case "response.output_item.done":
				done = append(done, ev.Item)
				continue
			case "response.output_text.delta":
				out = llm.StreamEvent{Type: llm.EventTextDelta, Text: ev.Delta}
			case "response.reasoning_summary_text.delta":
				out = llm.StreamEvent{Type: llm.EventThinkingDelta, Text: ev.Delta}
			case "response.reasoning_summary_part.done":
				out = llm.StreamEvent{Type: llm.EventThinkingDelta, Text: "\n\n"}
			case "response.output_item.added":
				if ev.Item.Type != "function_call" {
					continue
				}
				out = llm.StreamEvent{Type: llm.EventToolUseStart, Text: ev.Item.Name}
			case "response.completed", "response.incomplete":
				r := ev.Response
				final = &r
				continue
			case "response.failed":
				yield(llm.StreamEvent{}, classifyMessage(string(ev.Response.Error.Code), ev.Response.Error.Message))
				return
			case "error":
				yield(llm.StreamEvent{}, classifyMessage(ev.Code, ev.Message))
				return
			default:
				continue
			}
			if !yield(out, nil) {
				return
			}
		}
		if err := stream.Err(); err != nil {
			yield(llm.StreamEvent{}, convertErr(err))
			return
		}
		if final == nil {
			yield(llm.StreamEvent{}, errors.New("openai: stream ended without a completed response"))
			return
		}
		output := final.Output
		if len(output) == 0 {
			output = done
		}
		msg := convertOutput(output, req.Model, p.name)
		stop := llm.StopEnd
		switch {
		case len(msg.ToolUses()) > 0:
			stop = llm.StopToolUse
		case final.Status == "incomplete" && final.IncompleteDetails.Reason == "max_output_tokens":
			stop = llm.StopMaxTokens
		case final.Status == "incomplete":
			stop = llm.StopRefusal
		}
		cached := int(final.Usage.InputTokensDetails.CachedTokens)
		yield(llm.StreamEvent{
			Type:       llm.EventDone,
			Message:    msg,
			StopReason: stop,
			Usage: llm.Usage{
				Input:     int(final.Usage.InputTokens) - cached,
				Output:    int(final.Usage.OutputTokens),
				CacheRead: cached,
			},
		}, nil)
	}
}

// reasoningModel reports models that accept the `reasoning` parameter.
func reasoningModel(model string) bool {
	m := model
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if strings.Contains(m, "chat-latest") {
		return false
	}
	return strings.HasPrefix(m, "gpt-5") || strings.HasPrefix(m, "gpt-6") ||
		strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4") ||
		strings.Contains(m, "codex")
}

func (p *Provider) buildParams(req llm.Request) (responses.ResponseNewParams, []option.RequestOption) {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(req.Model),
		// Stateless: history is resent each turn, so reasoning must come back
		// encrypted to be replayable.
		Store: sdk.Bool(false),
	}
	if req.System != "" {
		params.Instructions = sdk.String(req.System)
	}
	if req.MaxTokens > 0 && !p.chatGPT {
		params.MaxOutputTokens = sdk.Int(int64(req.MaxTokens))
	}
	if reasoningModel(req.Model) {
		params.Include = []responses.ResponseIncludable{"reasoning.encrypted_content"}
		params.Reasoning = shared.ReasoningParam{Summary: shared.ReasoningSummaryAuto}
		if req.Effort != llm.EffortDefault {
			params.Reasoning.Effort = shared.ReasoningEffort(req.Effort)
		}
	} else if s := req.Sampling; !s.Empty() {
		// Reasoning models reject both parameters, so they are sent only
		// for the rest. The Responses API has no top_k; it is dropped.
		if s.Temperature != nil {
			params.Temperature = sdk.Float(*s.Temperature)
		}
		if s.TopP != nil {
			params.TopP = sdk.Float(*s.TopP)
		}
	}

	var tools []map[string]any
	for _, t := range req.Tools {
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  t.Schema,
			"strict":      false,
		})
	}
	opts := []option.RequestOption{option.WithJSONSet("input", inputItems(req, p.name))}
	if len(tools) > 0 {
		opts = append(opts, option.WithJSONSet("tools", tools))
	}
	if req.CacheKey != "" {
		params.PromptCacheKey = sdk.String(req.CacheKey)
		if p.chatGPT {
			// The ChatGPT backend ignores the body's key: without these
			// headers it assigns every request a fresh one, and no
			// request ever reads another's cache.
			opts = append(opts, option.WithHeader("session_id", req.CacheKey), option.WithHeader("conversation_id", req.CacheKey))
		}
	}
	return params, opts
}

func inputItems(req llm.Request, name string) []any {
	items := []any{}
	for _, m := range llm.RepairToolHistory(req.Messages) {
		blocks := llm.ReplayableBlocks(m, name, req.Model)
		if m.Role == llm.RoleUser {
			var content []map[string]any
			for _, b := range blocks {
				switch b.Type {
				case llm.BlockToolResult:
					var output any = b.Content
					if len(b.Images) > 0 {
						parts := []map[string]any{{"type": "input_text", "text": b.Content}}
						for _, img := range b.Images {
							parts = append(parts, map[string]any{"type": "input_image", "image_url": "data:" + img.MediaType + ";base64," + img.Data})
						}
						output = parts
					}
					items = append(items, map[string]any{"type": "function_call_output", "call_id": b.ID, "output": output})
				case llm.BlockText:
					content = append(content, map[string]any{"type": "input_text", "text": b.Text})
				case llm.BlockImage:
					content = append(content, map[string]any{"type": "input_image", "image_url": "data:" + b.MediaType + ";base64," + b.Data})
				case llm.BlockDocument:
					// The Responses API wants a filename alongside the
					// bytes; the attachment path is the honest one.
					name := b.Attachment
					if name == "" {
						name = "attachment.pdf"
					}
					content = append(content, map[string]any{
						"type":      "input_file",
						"filename":  filepath.Base(name),
						"file_data": "data:" + b.MediaType + ";base64," + b.Data,
					})
				}
			}
			if len(content) > 0 {
				items = append(items, map[string]any{"role": "user", "content": content})
			}
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case llm.BlockOpaque:
				items = append(items, json.RawMessage(b.Raw))
			case llm.BlockText:
				if b.Text != "" {
					items = append(items, map[string]any{"role": "assistant", "content": b.Text})
				}
			case llm.BlockToolUse:
				args := string(b.Input)
				if args == "" {
					args = "{}"
				}
				items = append(items, map[string]any{"type": "function_call", "call_id": b.ID, "name": b.Name, "arguments": args})
			}
		}
	}
	return items
}

func convertOutput(output []responses.ResponseOutputItemUnion, model, name string) llm.Message {
	msg := llm.Message{Role: llm.RoleAssistant, Model: model}
	for _, item := range output {
		switch item.Type {
		case "message":
			var text strings.Builder
			for _, c := range item.Content {
				if c.Type == "output_text" {
					text.WriteString(c.Text)
				} else if c.Type == "refusal" {
					text.WriteString(c.Refusal)
				}
			}
			msg.Blocks = append(msg.Blocks, llm.TextBlock(text.String()))
		case "reasoning":
			var summary strings.Builder
			for _, s := range item.Summary {
				summary.WriteString(s.Text)
			}
			// Display copy for the transcript; the opaque item is what replays.
			if summary.Len() > 0 {
				msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockThinking, Text: summary.String(), Provider: name + "-display"})
			}
			msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockOpaque, Provider: name, Raw: json.RawMessage(item.RawJSON())})
		case "function_call":
			args := item.Arguments.OfString
			input := json.RawMessage(args)
			if !json.Valid(input) {
				input = nil
			}
			msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockToolUse, ID: item.CallID, Name: item.Name, Input: input})
		}
	}
	return msg
}

func classifyMessage(code, message string) error {
	err := fmt.Errorf("openai: %s: %s", code, message)
	if code == "context_length_exceeded" {
		return fmt.Errorf("%w: %v", llm.ErrContextOverflow, err)
	}
	if code == "rate_limit_exceeded" || code == "server_error" {
		return &llm.APIError{Retryable: true, Err: err}
	}
	return err
}

func convertErr(err error) error {
	return openaisdk.ConvertError(err, func(apiErr *sdk.Error) bool {
		return apiErr.Code == "context_length_exceeded"
	})
}

// AcceptsDocuments reports PDF support: the Responses API takes an
// input_file, and the model reads the file itself.
func (p *Provider) AcceptsDocuments(mediaType string) bool {
	return mediaType == "application/pdf"
}
