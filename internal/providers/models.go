package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"larik/internal/chatgpt"
	"larik/internal/claudeagent"
	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/llm/anthropic"
	"larik/internal/llm/gemini"
	"larik/internal/llm/openai"
	"larik/internal/llm/openaicompat"
)

// Choice is a provider the setup wizard offers.
type Choice struct {
	Name    string // config key and model-spec prefix
	Title   string
	Desc    string
	KeyEnv  string // environment variable holding its key; empty for local servers
	BaseURL string // default endpoint
	Type    string // adapter type for catalog-discovered providers
	Local   bool
	SignIn  bool // authenticates by signing in (ChatGPT) instead of a key
	CLI     bool // authenticates through an installed vendor CLI
	Catwalk bool // discovered in Catwalk; endpoint must be confirmed by the user
}

// Codex runs OpenAI's Codex models on a ChatGPT plan, via sign-in.
const Codex = "codex"

const ClaudeCLI = "claude-code-cli"

// Choices lists built-in providers followed by supported Catwalk entries.
func Choices() []Choice {
	cs := builtinChoices()
	seen := make(map[string]bool, len(cs))
	for _, c := range cs {
		seen[c.Name] = true
	}
	for _, c := range discoveredChoices() {
		if !seen[c.Name] {
			cs = append(cs, c)
			seen[c.Name] = true
		}
	}
	return cs
}

func builtinChoices() []Choice {
	cs := []Choice{
		{Name: anthropic.Name, Title: "Anthropic", Desc: "Claude models", KeyEnv: "ANTHROPIC_API_KEY", BaseURL: "https://api.anthropic.com"},
		{Name: openai.Name, Title: "OpenAI", Desc: "GPT models", KeyEnv: "OPENAI_API_KEY", BaseURL: "https://api.openai.com/v1"},
		{Name: gemini.Name, Title: "Google Gemini", Desc: "Gemini models", KeyEnv: "GEMINI_API_KEY", BaseURL: "https://generativelanguage.googleapis.com"},
		{Name: Codex, Title: "ChatGPT (Codex)", Desc: "Codex models on your ChatGPT plan, via sign-in", BaseURL: chatgpt.BaseURL, SignIn: true},
		{Name: ClaudeCLI, Title: "Claude Code CLI", Desc: "Claude models using your Claude Code sign-in", CLI: true},
	}
	for _, c := range []struct{ name, title, desc string }{
		{"nvidia-nim", "NVIDIA NIM", "hosted models for development and testing"},
		{"openrouter", "OpenRouter", "many vendors behind one key"},
		{"groq", "Groq", "fast hosted open models"},
		{"deepseek", "DeepSeek", "DeepSeek models"},
		{"xai", "xAI", "Grok models"},
		{"mistral", "Mistral", "Mistral models"},
		{"together", "Together", "hosted open models"},
		{"ollama", "Ollama", "models running on this machine"},
		{"lmstudio", "LM Studio", "models running on this machine"},
	} {
		p := openaicompat.Presets[c.name]
		cs = append(cs, Choice{Name: c.name, Title: c.title, Desc: c.desc, KeyEnv: p.KeyEnv, BaseURL: p.BaseURL, Local: p.KeyEnv == ""})
	}
	return cs
}

// ChoiceFor returns the built-in choice named name.
func ChoiceFor(name string) (Choice, bool) {
	for _, c := range Choices() {
		if c.Name == name {
			return c, true
		}
	}
	return Choice{}, false
}

// EnvKey returns the key set in the environment for a built-in provider.
func EnvKey(name string) string {
	switch name {
	case anthropic.Name:
		if k := os.Getenv("ANTHROPIC_API_KEY"); k != "" {
			return k
		}
		return os.Getenv("ANTHROPIC_AUTH_TOKEN")
	case gemini.Name:
		return geminiKey()
	}
	if c, ok := ChoiceFor(name); ok && c.KeyEnv != "" {
		return os.Getenv(c.KeyEnv)
	}
	return ""
}

// Endpoint is where and how to reach a provider's API.
type Endpoint struct {
	Name    string
	Kind    string // anthropic, openai, gemini, codex, or an openai-compatible preset / "openai-compatible"
	BaseURL string
	Key     string
	// Token authenticates a signed-in provider (codex) per request.
	Token func(ctx context.Context) (token, account string, err error)
}

// EndpointFor resolves a provider's endpoint from config, presets and the
// environment, the same way the provider itself is built.
func EndpointFor(cfg *config.Config, name string) Endpoint {
	pc := cfg.Providers[name]
	e := EndpointOf(name, pc)
	if e.Kind == Codex && chatgpt.SignedIn(chatgpt.Path(cfg.ConfigDir)) {
		e.Token = chatgpt.NewSource(chatgpt.Path(cfg.ConfigDir)).Token
	}
	return e
}

// EndpointOf resolves the endpoint for an explicit provider config.
func EndpointOf(name string, pc config.ProviderConfig) Endpoint {
	kind := pc.Type
	if kind == "" {
		kind = name
		if c, ok := ChoiceFor(name); ok && c.Type != "" {
			kind = c.Type
		}
	}
	e := Endpoint{Name: name, Kind: kind, BaseURL: pc.BaseURL, Key: pc.APIKey}
	if e.Key == "" && pc.APIKeyEnv != "" {
		e.Key = os.Getenv(pc.APIKeyEnv)
	}
	if e.Key == "" {
		e.Key = EnvKey(name)
		if e.Key == "" && kind != name {
			e.Key = EnvKey(kind)
		}
	}
	if e.BaseURL == "" {
		choice, hasChoice := ChoiceFor(name)
		if hasChoice {
			e.BaseURL = choice.BaseURL
		}
		// A Catwalk entry with an unresolved endpoint must be configured by
		// the user. Never fall through to another provider's default URL.
		if e.BaseURL == "" && (!hasChoice || !choice.Catwalk) {
			if c, ok := ChoiceFor(kind); ok {
				e.BaseURL = c.BaseURL
			}
		}
	}
	return e
}

// Model is one entry in a provider's model list.
type Model struct {
	ID       string
	Size     int64  // bytes on disk, for local models
	Params   string // e.g. "4.0B"
	Context  int    // context window the model supports, if known
	Tools    bool
	Thinking bool
	Chat     bool
	// Efforts are the reasoning levels explicitly advertised by the provider.
	Efforts []llm.Effort
	// CapsKnown reports whether the server said what the model can do.
	// Otherwise Chat is a guess from the id and Tools/Thinking are unknown.
	CapsKnown bool
}

// ListModels asks the provider which models it serves.
func (e Endpoint) ListModels(ctx context.Context) ([]Model, error) {
	switch {
	case e.Kind == ClaudeCLI:
		status := claudeagent.New().Check(ctx)
		if !status.Ready() {
			return nil, errors.New(status.Detail)
		}
		// Claude Code has no supported non-interactive model-list command.
		// Offer its rolling aliases as useful defaults; arbitrary full model
		// IDs remain available through the picker's freeform entry.
		out := make([]Model, 0, len(claudeagent.SuggestedModels))
		for _, id := range claudeagent.SuggestedModels {
			out = append(out, Model{ID: id, Chat: true, Tools: true, Thinking: true, Efforts: []llm.Effort{
				llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax,
			}})
		}
		return out, nil
	case e.Kind == Codex:
		return e.codexModels(ctx)
	case e.Kind == anthropic.Name:
		return e.anthropicModels(ctx)
	case e.Kind == gemini.Name:
		return e.geminiModels(ctx)
	case e.IsOllama():
		return e.ollamaModels(ctx)
	}
	return e.openAIModels(ctx)
}

// IsOllama reports whether the endpoint is an Ollama server.
func (e Endpoint) IsOllama() bool {
	return e.Kind == "ollama" || strings.Contains(e.BaseURL, ":11434")
}

func (e Endpoint) ollamaModels(ctx context.Context) ([]Model, error) {
	var tags struct {
		Models []struct {
			Name    string `json:"name"`
			Size    int64  `json:"size"`
			Details struct {
				ParameterSize string `json:"parameter_size"`
				ContextLength int    `json:"context_length"`
			} `json:"details"`
			Capabilities []string `json:"capabilities"`
		} `json:"models"`
	}
	native := strings.TrimSuffix(strings.TrimRight(e.BaseURL, "/"), "/v1")
	if err := getJSON(ctx, native+"/api/tags", nil, &tags); err != nil {
		return nil, err
	}
	var out []Model
	for _, t := range tags.Models {
		m := Model{ID: t.Name, Size: t.Size, Params: t.Details.ParameterSize, Context: t.Details.ContextLength}
		if t.Capabilities != nil {
			m.CapsKnown = true
			m.Chat = slices.Contains(t.Capabilities, "completion")
			m.Tools = slices.Contains(t.Capabilities, "tools")
			m.Thinking = slices.Contains(t.Capabilities, "thinking")
		} else {
			m.Chat = likelyChat(t.Name)
		}
		out = append(out, m)
	}
	return out, nil
}

func (e Endpoint) openAIModels(ctx context.Context) ([]Model, error) {
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	h := map[string]string{}
	if e.Key != "" {
		h["Authorization"] = "Bearer " + e.Key
	}
	if err := getJSON(ctx, strings.TrimRight(e.BaseURL, "/")+"/models", h, &list); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(list.Data))
	for _, d := range list.Data {
		out = append(out, withCatalog(Model{ID: d.ID, Chat: likelyChat(d.ID)}))
	}
	sortModels(out)
	return out, nil
}

func (e Endpoint) anthropicModels(ctx context.Context) ([]Model, error) {
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	h := map[string]string{"anthropic-version": "2023-06-01"}
	switch {
	case e.Key != "" && e.Key == os.Getenv("ANTHROPIC_AUTH_TOKEN") && os.Getenv("ANTHROPIC_API_KEY") == "":
		h["Authorization"] = "Bearer " + e.Key
	case e.Key != "":
		h["x-api-key"] = e.Key
	}
	if err := getJSON(ctx, strings.TrimRight(e.BaseURL, "/")+"/v1/models?limit=100", h, &list); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(list.Data))
	for _, d := range list.Data {
		out = append(out, withCatalog(Model{ID: d.ID, Chat: true}))
	}
	return out, nil // newest first, as the API returns them
}

func (e Endpoint) geminiModels(ctx context.Context) ([]Model, error) {
	var list struct {
		Models []struct {
			Name             string   `json:"name"`
			InputTokenLimit  int      `json:"inputTokenLimit"`
			GenerationMethod []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	h := map[string]string{}
	if e.Key != "" {
		h["x-goog-api-key"] = e.Key
	}
	if err := getJSON(ctx, strings.TrimRight(e.BaseURL, "/")+"/v1beta/models?pageSize=200", h, &list); err != nil {
		return nil, err
	}
	var out []Model
	for _, g := range list.Models {
		out = append(out, Model{
			ID:      strings.TrimPrefix(g.Name, "models/"),
			Context: g.InputTokenLimit,
			Chat:    slices.Contains(g.GenerationMethod, "generateContent"),
		})
	}
	sortModels(out)
	return out, nil
}

// CodexModels are the Codex models offered, in Codex's order, when the
// account's own list can't be fetched.
var CodexModels = []string{"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5"}

// codexModels lists the models the signed-in ChatGPT account can use,
// falling back to CodexModels when the list isn't available.
func (e Endpoint) codexModels(ctx context.Context) ([]Model, error) {
	if e.Token == nil {
		return nil, errors.New("not signed in to ChatGPT; run /connect codex")
	}
	tok, account, err := e.Token(ctx)
	if err != nil {
		return nil, err
	}
	var list struct {
		Models []struct {
			Slug            string `json:"slug"`
			Visibility      string `json:"visibility"`
			ContextWindow   int    `json:"context_window"`
			ReasoningLevels []struct {
				Effort llm.Effort `json:"effort"`
			} `json:"supported_reasoning_levels"`
		} `json:"models"`
	}
	h := map[string]string{"Authorization": "Bearer " + tok, "chatgpt-account-id": account, "originator": "larik"}
	var out []Model
	if getJSON(ctx, strings.TrimRight(e.BaseURL, "/")+"/models?client_version=1.0.0", h, &list) == nil {
		for _, m := range list.Models {
			if m.Visibility == "list" {
				item := Model{ID: m.Slug, Context: m.ContextWindow, Chat: true, Tools: true, CapsKnown: true}
				for _, level := range m.ReasoningLevels {
					item.Efforts = append(item.Efforts, level.Effort)
				}
				out = append(out, item)
			}
		}
	}
	if len(out) == 0 {
		for _, id := range CodexModels {
			out = append(out, withCatalog(Model{ID: id, Chat: true, Tools: true, CapsKnown: true}))
		}
	}
	return out, nil
}

// PullProgress is one update from an Ollama download.
type PullProgress struct {
	Status    string // e.g. "pulling manifest", "verifying sha256 digest"
	Completed int64  // bytes of the current layer
	Total     int64
}

// Pull downloads model into an Ollama server, calling progress for each
// update. Cancel ctx to stop; Ollama keeps what it fetched for next time.
func (e Endpoint) Pull(ctx context.Context, model string, progress func(PullProgress)) error {
	if !e.IsOllama() {
		return fmt.Errorf("%s can't download models", e.Name)
	}
	native := strings.TrimSuffix(strings.TrimRight(e.BaseURL, "/"), "/v1")
	body, _ := json.Marshal(map[string]any{"model": model, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, native+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("can't reach %s (is the server running?)", req.URL.Host)
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(resp.Body)
	for {
		var line struct {
			Status    string `json:"status"`
			Completed int64  `json:"completed"`
			Total     int64  `json:"total"`
			Error     string `json:"error"`
		}
		if err := dec.Decode(&line); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				return fmt.Errorf("the download of %s ended early", model)
			}
			return err
		}
		switch {
		case line.Error != "":
			if strings.Contains(line.Error, "file does not exist") {
				return fmt.Errorf("ollama.com has no model named %s", model)
			}
			return errors.New(line.Error)
		case resp.StatusCode != http.StatusOK:
			return fmt.Errorf("%s: %s", req.URL.Host, resp.Status)
		case line.Status == "success":
			return nil
		}
		progress(PullProgress{Status: line.Status, Completed: line.Completed, Total: line.Total})
	}
}

// Suggestion is a model worth offering to download.
type Suggestion struct {
	ID   string
	Desc string
}

// OllamaSuggestions are tool-capable models the wizard offers to download.
var OllamaSuggestions = []Suggestion{
	{"qwen3:4b", "small and capable · tools, thinking"},
	{"qwen3:8b", "stronger, needs more memory · tools, thinking"},
	{"qwen2.5-coder:7b", "tuned for code · tools"},
	{"llama3.2:3b", "fast on modest machines · tools"},
}

// withCatalog fills in what the built-in catalog knows about a model.
func withCatalog(m Model) Model {
	if info, ok := llm.Catalog[m.ID]; ok && m.Context == 0 {
		m.Context = info.ContextWindow
	}
	return m
}

func sortModels(ms []Model) {
	slices.SortFunc(ms, func(a, b Model) int { return strings.Compare(a.ID, b.ID) })
}

// likelyChat guesses from the id whether a model can hold a conversation.
func likelyChat(id string) bool {
	id = strings.ToLower(id)
	for _, s := range []string{"embed", "tts", "whisper", "dall-e", "moderation", "transcribe", "image", "rerank"} {
		if strings.Contains(id, s) {
			return false
		}
	}
	return true
}

// Detection is what a quick look at a provider found.
type Detection struct {
	Name    string
	Running bool // local server answered
	Models  int
	HasKey  bool // a key is in the environment or config
}

// Detect probes the local servers and checks for keys, in parallel.
func Detect(ctx context.Context, cfg *config.Config) map[string]Detection {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out = map[string]Detection{}
	)
	for _, c := range Choices() {
		e := EndpointFor(cfg, c.Name)
		if !c.Local {
			out[c.Name] = Detection{Name: c.Name, HasKey: e.Key != "" || e.Token != nil}
			continue
		}
		wg.Go(func() {
			ms, err := e.ListModels(ctx)
			mu.Lock()
			out[c.Name] = Detection{Name: c.Name, Running: err == nil, Models: len(ms)}
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

// Usable lists the providers worth showing models for: configured ones,
// built-ins with a key available, and local presets (which the caller
// drops if they don't answer).
func Usable(cfg *config.Config) []string {
	var names []string
	seen := map[string]bool{}
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, c := range Choices() {
		_, configured := cfg.Providers[c.Name]
		signedIn := c.SignIn && chatgpt.SignedIn(chatgpt.Path(cfg.ConfigDir))
		keyReady := EnvKey(c.Name) != "" && (!c.Catwalk || c.BaseURL != "")
		if configured || c.Local || c.CLI || keyReady || signedIn {
			add(c.Name)
		}
	}
	custom := make([]string, 0, len(cfg.Providers))
	for n := range cfg.Providers {
		custom = append(custom, n)
	}
	slices.Sort(custom)
	for _, n := range custom {
		add(n)
	}
	return names
}

// HasDefault reports whether Resolve can pick a model without a spec.
func HasDefault(cfg *config.Config) bool {
	if cfg.Model != "" {
		return true
	}
	_, err := defaultSpec()
	return err == nil
}

func getJSON(ctx context.Context, url string, headers map[string]string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return fmt.Errorf("can't reach %s (is the server running?)", req.URL.Host)
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("%s did not answer in time", req.URL.Host)
		}
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("the API key was rejected (HTTP %d)", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("%s: %s %s", req.URL.Host, resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
