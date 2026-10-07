// Package gemini adapts the Google Gen AI SDK to llm.Provider.
package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	"larik/internal/llm"
)

const Name = "gemini"

const DefaultModel = "gemini-3.8-flash"

type Provider struct {
	apiKey  string
	baseURL string

	once   sync.Once
	client *genai.Client
	err    error
}

// New defers client creation to the first call so construction never fails.
func New(apiKey, baseURL string) *Provider { return &Provider{apiKey: apiKey, baseURL: baseURL} }

func (p *Provider) Name() string { return Name }

func (p *Provider) Stream(ctx context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		p.once.Do(func() {
			cc := &genai.ClientConfig{APIKey: p.apiKey, Backend: genai.BackendGeminiAPI, HTTPClient: llm.HTTPClient}
			if p.baseURL != "" {
				cc.HTTPOptions.BaseURL = p.baseURL
			}
			p.client, p.err = genai.NewClient(context.Background(), cc)
		})
		if p.err != nil {
			yield(llm.StreamEvent{}, p.err)
			return
		}

		msg := llm.Message{Role: llm.RoleAssistant, Model: req.Model}
		var (
			usage  llm.Usage
			finish genai.FinishReason
			nCalls int
		)
		for resp, err := range p.client.Models.GenerateContentStream(ctx, req.Model, contents(req), config(req)) {
			if err != nil {
				yield(llm.StreamEvent{}, convertErr(err))
				return
			}
			if u := resp.UsageMetadata; u != nil {
				cached := int(u.CachedContentTokenCount)
				usage = llm.Usage{
					Input:     int(u.PromptTokenCount) - cached,
					Output:    int(u.CandidatesTokenCount + u.ThoughtsTokenCount),
					CacheRead: cached,
				}
			}
			if len(resp.Candidates) == 0 {
				continue
			}
			cand := resp.Candidates[0]
			if cand.FinishReason != "" {
				finish = cand.FinishReason
			}
			if cand.Content == nil {
				continue
			}
			for _, part := range cand.Content.Parts {
				ev, ok := appendPart(&msg, part, &nCalls)
				if ok && !yield(ev, nil) {
					return
				}
			}
		}

		stop := llm.StopEnd
		switch {
		case nCalls > 0:
			stop = llm.StopToolUse
		case finish == genai.FinishReasonMaxTokens:
			stop = llm.StopMaxTokens
		case finish != "" && finish != genai.FinishReasonStop:
			stop = llm.StopRefusal
		}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: stop, Usage: usage}, nil)
	}
}

// appendPart merges one streamed part into msg and returns the display event.
func appendPart(msg *llm.Message, part *genai.Part, nCalls *int) (llm.StreamEvent, bool) {
	sig := ""
	if len(part.ThoughtSignature) > 0 {
		sig = base64.StdEncoding.EncodeToString(part.ThoughtSignature)
	}
	last := func(t llm.BlockType) *llm.Block {
		if n := len(msg.Blocks); n > 0 && msg.Blocks[n-1].Type == t {
			return &msg.Blocks[n-1]
		}
		return nil
	}
	switch {
	case part.FunctionCall != nil:
		fc := part.FunctionCall
		id := fc.ID
		if id == "" {
			id = llm.NewCallID("gemini_call_")
		}
		*nCalls++
		args, _ := json.Marshal(fc.Args)
		if fc.Args == nil {
			args = []byte(`{}`)
		}
		msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockToolUse, ID: id, Name: fc.Name, Input: args, Signature: sig, Provider: Name})
		return llm.StreamEvent{Type: llm.EventToolUseStart, Text: fc.Name}, true
	case part.Thought:
		if b := last(llm.BlockThinking); b != nil {
			b.Text += part.Text
		} else {
			// Thought summaries are display-only; signatures ride on other parts.
			msg.Blocks = append(msg.Blocks, llm.Block{Type: llm.BlockThinking, Text: part.Text, Provider: Name + "-display"})
		}
		return llm.StreamEvent{Type: llm.EventThinkingDelta, Text: part.Text}, part.Text != ""
	case part.Text != "" || sig != "":
		if b := last(llm.BlockText); b != nil && (sig == "" || b.Signature == "") {
			b.Text += part.Text
			if sig != "" {
				b.Signature, b.Provider = sig, Name
			}
		} else {
			b := llm.TextBlock(part.Text)
			if sig != "" {
				b.Signature, b.Provider = sig, Name
			}
			msg.Blocks = append(msg.Blocks, b)
		}
		return llm.StreamEvent{Type: llm.EventTextDelta, Text: part.Text}, part.Text != ""
	}
	return llm.StreamEvent{}, false
}

func config(req llm.Request) *genai.GenerateContentConfig {
	cfg := &genai.GenerateContentConfig{
		ThinkingConfig: &genai.ThinkingConfig{IncludeThoughts: true},
	}
	if req.System != "" {
		cfg.SystemInstruction = &genai.Content{Parts: []*genai.Part{{Text: req.System}}}
	}
	if req.MaxTokens > 0 {
		cfg.MaxOutputTokens = int32(req.MaxTokens)
	}
	switch req.Effort {
	case llm.EffortLow:
		cfg.ThinkingConfig.ThinkingLevel = genai.ThinkingLevelLow
	case llm.EffortMedium:
		cfg.ThinkingConfig.ThinkingLevel = genai.ThinkingLevelMedium
	case llm.EffortHigh, llm.EffortXHigh, llm.EffortMax:
		cfg.ThinkingConfig.ThinkingLevel = genai.ThinkingLevelHigh
	}
	if s := req.Sampling; !s.Empty() {
		if s.Temperature != nil {
			cfg.Temperature = genai.Ptr(float32(*s.Temperature))
		}
		if s.TopP != nil {
			cfg.TopP = genai.Ptr(float32(*s.TopP))
		}
		if s.TopK != nil {
			cfg.TopK = genai.Ptr(float32(*s.TopK))
		}
	}
	if len(req.Tools) > 0 {
		var decls []*genai.FunctionDeclaration
		for _, t := range req.Tools {
			decls = append(decls, &genai.FunctionDeclaration{Name: t.Name, Description: t.Description, ParametersJsonSchema: t.Schema})
		}
		cfg.Tools = []*genai.Tool{{FunctionDeclarations: decls}}
	}
	return cfg
}

func contents(req llm.Request) []*genai.Content {
	var out []*genai.Content
	for _, m := range llm.RepairToolHistory(req.Messages) {
		sameProducer := m.Model == req.Model
		c := &genai.Content{Role: genai.RoleUser}
		if m.Role == llm.RoleAssistant {
			c.Role = genai.RoleModel
		}
		for _, b := range llm.ReplayableBlocks(m, Name, req.Model) {
			var part *genai.Part
			switch b.Type {
			case llm.BlockText:
				if b.Text == "" && b.Signature == "" {
					continue
				}
				part = &genai.Part{Text: b.Text}
			case llm.BlockToolUse:
				var args map[string]any
				_ = json.Unmarshal(b.Input, &args)
				part = &genai.Part{FunctionCall: &genai.FunctionCall{ID: geminiID(b.ID), Name: b.Name, Args: args}}
			case llm.BlockToolResult:
				key := "output"
				if b.IsError {
					key = "error"
				}
				part = &genai.Part{FunctionResponse: &genai.FunctionResponse{
					ID: geminiID(b.ID), Name: b.Name, Response: map[string]any{key: b.Content},
				}}
				// Images as sibling parts, not FunctionResponse.Parts, which
				// only Gemini 3 models accept.
				if imgs := inlineImages(b.Images); len(imgs) > 0 {
					c.Parts = append(c.Parts, part)
					c.Parts = append(c.Parts, imgs...)
					continue
				}
			case llm.BlockImage:
				data, err := base64.StdEncoding.DecodeString(b.Data)
				if err != nil {
					continue
				}
				part = &genai.Part{InlineData: &genai.Blob{MIMEType: b.MediaType, Data: data}}
			default:
				continue
			}
			if sameProducer && b.Provider == Name && b.Signature != "" {
				part.ThoughtSignature, _ = base64.StdEncoding.DecodeString(b.Signature)
			}
			c.Parts = append(c.Parts, part)
		}
		if len(c.Parts) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// inlineImages converts image blocks to inline data parts, skipping any
// that don't decode.
func inlineImages(blocks []llm.Block) []*genai.Part {
	var parts []*genai.Part
	for _, b := range blocks {
		if data, err := base64.StdEncoding.DecodeString(b.Data); err == nil {
			parts = append(parts, &genai.Part{InlineData: &genai.Blob{MIMEType: b.MediaType, Data: data}})
		}
	}
	return parts
}

// geminiID drops ids we synthesized; Gemini matches those by name and order.
func geminiID(id string) string {
	if strings.HasPrefix(id, "gemini_call_") {
		return ""
	}
	return id
}

func convertErr(err error) error {
	var apiErr genai.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code == 400 && strings.Contains(strings.ToLower(apiErr.Message), "token") && strings.Contains(strings.ToLower(apiErr.Message), "exceed") {
			return fmt.Errorf("%w: %v", llm.ErrContextOverflow, err)
		}
		classified := llm.ClassifyStatus(apiErr.Code, err)
		classified.(*llm.APIError).RetryAfter = retryDelay(apiErr.Details)
		return classified
	}
	return err
}

// retryDelay reads the wait a google.rpc.RetryInfo detail asks for.
func retryDelay(details []map[string]any) time.Duration {
	for _, d := range details {
		if t, _ := d["@type"].(string); strings.HasSuffix(t, "google.rpc.RetryInfo") {
			if s, ok := d["retryDelay"].(string); ok {
				if wait, err := time.ParseDuration(s); err == nil && wait > 0 {
					return wait
				}
			}
		}
	}
	return 0
}
