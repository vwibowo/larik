package providers

import (
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
	Local   bool
}

// Choices lists the built-in providers in the order the wizard shows them.
func Choices() []Choice {
	cs := []Choice{
		{Name: anthropic.Name, Title: "Anthropic", Desc: "Claude models", KeyEnv: "ANTHROPIC_API_KEY", BaseURL: "https://api.anthropic.com"},
		{Name: openai.Name, Title: "OpenAI", Desc: "GPT models", KeyEnv: "OPENAI_API_KEY", BaseURL: "https://api.openai.com/v1"},
		{Name: gemini.Name, Title: "Google Gemini", Desc: "Gemini models", KeyEnv: "GEMINI_API_KEY", BaseURL: "https://generativelanguage.googleapis.com"},
	}
	for _, c := range []struct{ name, title, desc string }{
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
	Kind    string // anthropic, openai, gemini, or an openai-compatible preset / "openai-compatible"
	BaseURL string
	Key     string
}

// EndpointFor resolves a provider's endpoint from config, presets and the
// environment, the same way the provider itself is built.
func EndpointFor(cfg *config.Config, name string) Endpoint {
	pc := cfg.Providers[name]
	return EndpointOf(name, pc)
}

// EndpointOf resolves the endpoint for an explicit provider config.
func EndpointOf(name string, pc config.ProviderConfig) Endpoint {
	kind := pc.Type
	if kind == "" {
		kind = name
	}
	e := Endpoint{Name: name, Kind: kind, BaseURL: pc.BaseURL, Key: pc.APIKey}
	if e.Key == "" && pc.APIKeyEnv != "" {
		e.Key = os.Getenv(pc.APIKeyEnv)
	}
	if e.Key == "" {
		e.Key = EnvKey(kind)
	}
	if e.BaseURL == "" {
		if c, ok := ChoiceFor(kind); ok {
			e.BaseURL = c.BaseURL
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
	// CapsKnown reports whether the server said what the model can do.
	// Otherwise Chat is a guess from the id and Tools/Thinking are unknown.
	CapsKnown bool
}

// ListModels asks the provider which models it serves.
func (e Endpoint) ListModels(ctx context.Context) ([]Model, error) {
	switch {
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
			out[c.Name] = Detection{Name: c.Name, HasKey: e.Key != ""}
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
		if _, ok := cfg.Providers[c.Name]; ok || c.Local || EnvKey(c.Name) != "" {
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
