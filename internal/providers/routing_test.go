package providers

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"testing"

	"larik/internal/config"
	"larik/internal/llm"
)

func routingConfig(roles map[string]string, fallbacks map[string][]string) *config.Config {
	return &config.Config{
		Model:     "anthropic/claude-opus-5",
		Providers: map[string]config.ProviderConfig{},
		Roles:     roles,
		Fallbacks: fallbacks,
	}
}

func TestResolveRoles(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "ak")
	t.Setenv("GROQ_API_KEY", "gk")
	cfg := routingConfig(map[string]string{"worker": "groq/llama-4-scout", "sonnet": "groq/qwen3-32b", "loop": "worker"}, nil)

	for spec, want := range map[string]string{
		"worker":                    "groq/llama-4-scout",
		"explore":                   "anthropic/claude-opus-5", // unset: inherits the main model
		"sonnet":                    "groq/qwen3-32b",          // a legacy alias the user mapped
		"haiku":                     "anthropic/claude-haiku-4-5",
		"groq/moonshotai/kimi-k2.5": "groq/moonshotai/kimi-k2.5",
	} {
		r, err := Resolve(cfg, spec)
		if err != nil {
			t.Errorf("%s: %v", spec, err)
			continue
		}
		if got := r.Provider.Name() + "/" + r.Model; got != want {
			t.Errorf("%s resolved to %s, want %s", spec, got, want)
		}
	}
	if _, err := Resolve(cfg, "loop"); err == nil || !strings.Contains(err.Error(), "not another role") {
		t.Errorf("a role naming a role: err = %v", err)
	}
	cfg.Model = "worker"
	cfg.Roles["worker"] = ""
	if _, err := Resolve(cfg, ""); err == nil {
		t.Errorf("a main model naming an unset role must fail, not recurse")
	}
}

func TestRoleNamesAndAliases(t *testing.T) {
	cfg := routingConfig(map[string]string{"reviewer": "openai/gpt-5.5", "haiku": "groq/x"}, nil)
	if got := strings.Join(RoleNames(cfg), ","); got != "smart,worker,explore,compact,haiku,reviewer" {
		t.Errorf("RoleNames = %s", got)
	}
	if !IsLegacyAlias(cfg, "opus") || IsLegacyAlias(cfg, "haiku") || IsLegacyAlias(cfg, "worker") {
		t.Errorf("only unmapped Claude aliases are legacy")
	}
	if IsRole(cfg, "openai/gpt-5.5") || !IsRole(cfg, "explore") {
		t.Errorf("IsRole confuses roles and specs")
	}
}

func TestResolveWrapsFallbacks(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "gk")
	t.Setenv("DEEPSEEK_API_KEY", "")
	cfg := routingConfig(
		map[string]string{"worker": "groq/llama-4-scout"},
		map[string][]string{
			"worker":             {"deepseek/deepseek-chat", "ollama/qwen3-coder", "explore"}, // deepseek has no key; roles are skipped
			"groq/llama-4-scout": {"lmstudio/never-used"},                                     // the role's list wins
		})
	r, err := Resolve(cfg, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if r.Provider.Name() != "groq" || r.Model != "llama-4-scout" {
		t.Fatalf("resolved %s/%s", r.Provider.Name(), r.Model)
	}
	wrapped := func(p llm.Provider) bool { return fmt.Sprintf("%T", p) == "*llm.fallback" }
	if !wrapped(r.Provider) {
		t.Fatalf("the worker should be wrapped in a fallback chain, got %T", r.Provider)
	}
	// Keyed by spec when resolved by spec.
	r, _ = Resolve(cfg, "groq/llama-4-scout")
	if !wrapped(r.Provider) {
		t.Errorf("a spec with fallbacks should be wrapped, got %T", r.Provider)
	}
	// No chain, no wrapper.
	r, _ = Resolve(cfg, "groq/other")
	if wrapped(r.Provider) {
		t.Errorf("a spec without fallbacks should not be wrapped")
	}
}

// failing always fails before output with err.
type failing struct {
	name string
	err  error
}

func (f failing) Name() string { return f.name }
func (f failing) Stream(context.Context, llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) { yield(llm.StreamEvent{}, f.err) }
}

func TestFallbackChainOrder(t *testing.T) {
	var got []string
	rec := func(name string) llm.Provider {
		return failing{name, llm.ClassifyStatus(429, fmt.Errorf("%s limited", name))}
	}
	p := llm.WithFallback(llm.Candidate{Provider: rec("a"), Model: "1"}, llm.Candidate{Provider: rec("b"), Model: "2"}, llm.Candidate{Provider: rec("c"), Model: "3"})
	var last error
	for ev, err := range p.Stream(context.Background(), llm.Request{Model: "1"}) {
		if err != nil {
			last = err
			break
		}
		got = append(got, ev.Text)
	}
	if len(got) != 2 || !strings.Contains(got[1], "c/3") || last == nil || !strings.Contains(last.Error(), "c limited") {
		t.Errorf("notices = %q, err = %v", got, last)
	}
	var apiErr *llm.APIError
	if !errors.As(last, &apiErr) {
		t.Errorf("the final error should keep its type")
	}
}

func opts(specs ...string) []ModelOption {
	var out []ModelOption
	for _, s := range specs {
		p, id, _ := strings.Cut(s, "/")
		md := Model{ID: id, Chat: true}
		if name, params, ok := strings.Cut(id, "@"); ok {
			md.ID, md.Params = name, params
		}
		if strings.HasSuffix(md.ID, "-notools") {
			md.CapsKnown, md.Tools = true, false
		}
		out = append(out, ModelOption{Provider: p, Model: md})
	}
	return out
}

func TestSuggestRouting(t *testing.T) {
	available := opts(
		"anthropic/claude-opus-5",
		"anthropic/claude-haiku-4-5",
		"anthropic/claude-sonnet-5",
		"gemini/gemini-3.8-flash",
		"ollama/qwen3:4b@4.0B",
		"ollama/qwen3-coder@30.5B",
		"ollama/tiny-notools@1B",
	)
	main := "anthropic/claude-opus-5"

	bal := SuggestRouting(PresetBalanced, available, main)
	// Local models cost nothing per call, so they rank first; the 4B one
	// is fine for searching but too small for the worker.
	if bal.Delegation != config.DelegationBalanced {
		t.Errorf("balanced delegation = %q", bal.Delegation)
	}
	if bal.Roles["explore"] != "ollama/qwen3:4b" || bal.Roles["worker"] != "ollama/qwen3-coder" || bal.Roles["smart"] != main {
		t.Errorf("balanced roles = %v", bal.Roles)
	}
	if o := bal.Options["worker"]; o.Isolation != "worktree" || o.MaxTurns != DefaultWorkerTurns || o.Context != "minimal" {
		t.Errorf("worker options = %+v", o)
	}
	if o := bal.Options["explore"]; o.Isolation != "" || o.MaxTurns != DefaultExploreTurns || o.Context != "minimal" {
		t.Errorf("explore options = %+v; a read-only role needs no worktree, but still a smaller prompt", o)
	}
	if fb := bal.Fallbacks["worker"]; len(fb) != 1 || strings.HasPrefix(fb[0], "ollama/") || fb[0] == main {
		t.Errorf("worker fallback = %v, want one model on another provider", fb)
	}

	cloud := opts("anthropic/claude-opus-5", "anthropic/claude-haiku-4-5", "anthropic/claude-sonnet-5", "gemini/gemini-3.8-flash")
	cheap := SuggestRouting(PresetCheapest, cloud, main)
	if cheap.Delegation != config.DelegationAggressive {
		t.Errorf("cheapest delegation = %q", cheap.Delegation)
	}
	if cheap.Roles["worker"] != "gemini/gemini-3.8-flash" || cheap.Roles["smart"] != "" {
		t.Errorf("cheapest roles = %v", cheap.Roles)
	}
	if fb := cheap.Fallbacks["worker"]; len(fb) != 1 || fb[0] != "anthropic/claude-haiku-4-5" {
		t.Errorf("cheapest worker fallback = %v", fb)
	}

	// Nothing cheaper than a local main model: balanced leaves roles unset.
	if r := SuggestRouting(PresetBalanced, opts("ollama/qwen3-coder@30.5B"), "ollama/qwen3-coder"); len(r.Roles) != 0 {
		t.Errorf("single local model: roles = %v, want none", r.Roles)
	}
	if r := SuggestRouting(PresetCustom, available, main); len(r.Roles)+len(r.Fallbacks) != 0 || r.Delegation != "" {
		t.Errorf("custom should suggest nothing, got %+v", r)
	}

	// Real-world lists: a tiny local model, and several plan models.
	mixed := opts("ollama/qwen2.5:0.5b@494.03M", "ollama/qwen3:4b@4.0B", "ollama/qwen3-coder:latest@30.5B",
		"codex/gpt-5.5", "codex/gpt-6-astra", "codex/gpt-6-luna")
	r := SuggestRouting(PresetBalanced, mixed, "codex/gpt-6-astra")
	if r.Roles["explore"] != "ollama/qwen3:4b" || r.Roles["worker"] != "ollama/qwen3-coder:latest" {
		t.Errorf("a sub-3B model should never explore: %v", r.Roles)
	}
	if fb := r.Fallbacks["worker"]; len(fb) != 1 || fb[0] != "codex/gpt-6-luna" {
		t.Errorf("the plan fallback should be its light model: %v", fb)
	}

	withCodex := append(cloud, opts("codex/gpt-6-luna")...)
	if r := SuggestRouting(PresetLocal, withCodex, main); r.Roles["worker"] != "codex/gpt-6-luna" {
		t.Errorf("local-first should prefer the ChatGPT plan when nothing is local, got %v", r.Roles)
	}
}

func TestPriceNote(t *testing.T) {
	if got := PriceNote("anthropic/claude-haiku-4-5"); got != "$1/$5 per M" {
		t.Errorf("PriceNote = %q", got)
	}
	if got := PriceNote("openrouter/anthropic/claude-sonnet-5"); got != "$2/$10 per M" {
		t.Errorf("PriceNote via a router = %q", got)
	}
	if got := PriceNote("ollama/qwen3-coder"); got != "" {
		t.Errorf("unpriced model: %q", got)
	}
}
