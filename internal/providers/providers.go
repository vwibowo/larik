// Package providers builds llm.Provider instances from config and resolves
// "provider/model" strings.
package providers

import (
	"fmt"
	"os"
	"strings"

	"larik/internal/chatgpt"
	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/llm/anthropic"
	"larik/internal/llm/gemini"
	"larik/internal/llm/ollama"
	"larik/internal/llm/openai"
	"larik/internal/llm/openaicompat"
)

// Resolved is a ready-to-use provider and model.
type Resolved struct {
	Provider llm.Provider
	Model    string
}

func (r Resolved) String() string { return r.Provider.Name() + "/" + r.Model }

// Resolve turns a model spec into a provider. An empty spec picks a default
// from whichever API key is present.
func Resolve(cfg *config.Config, spec string) (Resolved, error) {
	if spec == "" {
		spec = cfg.Model
	}
	if spec == "" {
		var err error
		if spec, err = defaultSpec(); err != nil {
			return Resolved{}, err
		}
	}
	role := ""
	if rs, ok := RoleSpec(cfg, spec); ok {
		role = spec
		if rs == "" && cfg.Model != spec {
			return Resolve(cfg, "") // an unset role inherits the main model
		}
		if rs == "" || IsRole(cfg, rs) {
			return Resolved{}, fmt.Errorf("role %q must name a provider/model, not another role", spec)
		}
		spec = rs
	}
	r, err := resolveSpec(cfg, spec)
	if err != nil {
		return Resolved{}, err
	}
	return withFallbacks(cfg, r, role, spec), nil
}

// resolveSpec builds the provider for a plain provider/model spec.
func resolveSpec(cfg *config.Config, spec string) (Resolved, error) {
	name, model := split(cfg, spec)
	if model == "" {
		return Resolved{}, fmt.Errorf("model spec %q has no model id", spec)
	}
	p, err := build(cfg, name)
	if err != nil {
		return Resolved{}, err
	}
	return Resolved{Provider: p, Model: model}, nil
}

func defaultSpec() (string, error) {
	switch {
	case os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "":
		return anthropic.Name + "/" + anthropic.DefaultModel, nil
	case os.Getenv("OPENAI_API_KEY") != "":
		return openai.Name + "/" + openai.DefaultModel, nil
	case geminiKey() != "":
		return gemini.Name + "/" + gemini.DefaultModel, nil
	}
	return "", fmt.Errorf("no model configured and no API key found.\n" +
		"Run larik in a terminal to set one up, set ANTHROPIC_API_KEY, OPENAI_API_KEY or GEMINI_API_KEY,\n" +
		"or pass --model provider/model (e.g. ollama/qwen3-coder)")
}

func geminiKey() string {
	if k := os.Getenv("GEMINI_API_KEY"); k != "" {
		return k
	}
	return os.Getenv("GOOGLE_API_KEY")
}

// split parses "provider/model". A bare model id is matched to a provider
// by catalog entry or naming convention.
func split(cfg *config.Config, spec string) (provider, model string) {
	if i := strings.IndexByte(spec, '/'); i > 0 {
		head := spec[:i]
		if _, ok := cfg.Providers[head]; ok || isBuiltin(head) {
			return head, spec[i+1:]
		}
	}
	if m, ok := llm.Catalog[spec]; ok && m.Provider != "" {
		return m.Provider, spec
	}
	switch {
	case strings.HasPrefix(spec, "claude-"):
		return anthropic.Name, spec
	case strings.HasPrefix(spec, "gemini-"):
		return gemini.Name, spec
	case strings.HasPrefix(spec, "gpt-"), strings.HasPrefix(spec, "o1"), strings.HasPrefix(spec, "o3"), strings.HasPrefix(spec, "o4"), strings.Contains(spec, "codex"):
		return openai.Name, spec
	}
	return "", spec
}

func isBuiltin(name string) bool {
	switch name {
	case anthropic.Name, openai.Name, gemini.Name, Codex:
		return true
	}
	_, ok := openaicompat.Presets[name]
	return ok
}

// Names lists the built-in provider names for help text.
func Names() []string {
	names := []string{anthropic.Name, openai.Name, gemini.Name, Codex}
	for n := range openaicompat.Presets {
		names = append(names, n)
	}
	return names
}

func build(cfg *config.Config, name string) (llm.Provider, error) {
	if name == "" {
		return nil, fmt.Errorf("cannot tell which provider serves this model; use provider/model (providers: %s)", strings.Join(Names(), ", "))
	}
	pc := cfg.Providers[name]
	key := pc.APIKey
	if key == "" && pc.APIKeyEnv != "" {
		key = os.Getenv(pc.APIKeyEnv)
	}
	kind := pc.Type
	if kind == "" {
		kind = name
	}
	switch kind {
	case anthropic.Name:
		return anthropic.New(key, pc.BaseURL), nil
	case openai.Name:
		return openai.New(key, pc.BaseURL), nil
	case gemini.Name:
		if key == "" {
			key = geminiKey()
		}
		if key == "" {
			return nil, fmt.Errorf("gemini: set GEMINI_API_KEY")
		}
		return llm.WithRetry(gemini.New(key, pc.BaseURL), 4), nil
	}
	if kind == Codex {
		path := chatgpt.Path(cfg.ConfigDir)
		if !chatgpt.SignedIn(path) {
			return nil, fmt.Errorf("codex: not signed in to ChatGPT; run /connect codex")
		}
		baseURL := pc.BaseURL
		if baseURL == "" {
			baseURL = chatgpt.BaseURL
		}
		return openai.NewChatGPT(name, baseURL, chatgpt.NewSource(path).Token), nil
	}
	if kind == ollama.Name {
		baseURL := pc.BaseURL
		if baseURL == "" {
			baseURL = openaicompat.Presets[ollama.Name].BaseURL
		}
		return ollama.New(name, baseURL, pc.ContextLength), nil
	}
	preset, isPreset := openaicompat.Presets[kind]
	if kind != "openai-compatible" && !isPreset {
		return nil, fmt.Errorf("unknown provider %q (known: %s; or configure type \"openai-compatible\")", name, strings.Join(Names(), ", "))
	}
	baseURL := pc.BaseURL
	if baseURL == "" {
		baseURL = preset.BaseURL
	}
	if baseURL == "" {
		return nil, fmt.Errorf("provider %q needs base_url in config", name)
	}
	if key == "" && preset.KeyEnv != "" {
		key = os.Getenv(preset.KeyEnv)
		if key == "" {
			return nil, fmt.Errorf("%s: set %s", name, preset.KeyEnv)
		}
	}
	return openaicompat.New(name, key, baseURL), nil
}
