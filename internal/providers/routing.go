package providers

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/llm/anthropic"
)

// Role names with a meaning in larik. Users may add their own.
const (
	RoleSmart   = "smart"   // hard problems, design, review
	RoleWorker  = "worker"  // well-specified edits, tests, boilerplate
	RoleExplore = "explore" // read-only searches
	RoleCompact = "compact" // summarizing the conversation
)

// Turn caps the presets give cheap roles, instead of the usual 100.
const (
	DefaultWorkerTurns  = 40
	DefaultExploreTurns = 30
)

// Roles lists the built-in roles in display order.
var Roles = []string{RoleSmart, RoleWorker, RoleExplore, RoleCompact}

// RoleHints says what each built-in role is for, for the task tool and
// the setup screens.
var RoleHints = map[string]string{
	RoleSmart:   "hard problems, design decisions, careful review",
	RoleWorker:  "well-specified edits, tests, boilerplate, mechanical changes",
	RoleExplore: "read-only searches and summaries of code",
	RoleCompact: "summarizing the conversation when the context is full",
}

// legacyAliases are Claude Code's model aliases. They act as roles that
// default to these Claude models unless configured.
var legacyAliases = map[string]string{
	"opus":   anthropic.Name + "/claude-opus-5",
	"sonnet": anthropic.Name + "/claude-sonnet-5",
	"haiku":  anthropic.Name + "/claude-haiku-4-5",
}

// RoleSpec returns the spec configured for role name, and whether name is
// a role at all. An empty spec means the role inherits the main model.
func RoleSpec(cfg *config.Config, name string) (spec string, ok bool) {
	return roleSpec(cfg.Routing(), name)
}

func roleSpec(r config.Routing, name string) (string, bool) {
	if s, ok := r.Roles[name]; ok {
		return s, true
	}
	if slices.Contains(Roles, name) {
		return "", true
	}
	if s, ok := legacyAliases[name]; ok {
		return s, true
	}
	return "", false
}

// IsRole reports whether name is a role (built-in, configured or legacy
// alias) rather than a model spec.
func IsRole(cfg *config.Config, name string) bool {
	_, ok := RoleSpec(cfg, name)
	return ok
}

// IsLegacyAlias reports a Claude alias (opus, sonnet, haiku) that the
// config doesn't map to a model of its own.
func IsLegacyAlias(cfg *config.Config, name string) bool {
	_, configured := cfg.Routing().Roles[name]
	_, legacy := legacyAliases[name]
	return legacy && !configured
}

// RoleNames lists the built-in roles, then configured ones, sorted.
func RoleNames(cfg *config.Config) []string {
	names := slices.Clone(Roles)
	var extra []string
	for n := range cfg.Routing().Roles {
		if !slices.Contains(names, n) {
			extra = append(extra, n)
		}
	}
	slices.Sort(extra)
	return append(names, extra...)
}

// withFallbacks wraps r in the fallback chain configured for its role or
// spec. Candidates that can't be built (no key, say) are skipped.
func withFallbacks(cfg *config.Config, r Resolved, role, spec string) Resolved {
	fb := cfg.Routing().Fallbacks
	list, ok := fb[role]
	if !ok || role == "" {
		list = fb[spec]
	}
	var others []llm.Candidate
	for _, s := range list {
		if IsRole(cfg, s) {
			continue // chains name models, not roles
		}
		c, err := resolveSpec(cfg, s)
		if err != nil || (c.Provider.Name() == r.Provider.Name() && c.Model == r.Model) {
			continue
		}
		others = append(others, llm.Candidate{Provider: c.Provider, Model: c.Model})
	}
	r.Provider = llm.WithFallback(llm.Candidate{Provider: r.Provider, Model: r.Model}, others...)
	return r
}

// Preset is a starting point for the routing setup.
type Preset int

const (
	PresetBalanced Preset = iota // strong main model, cheap workers
	PresetCheapest               // the cheapest capable model everywhere
	PresetLocal                  // local or plan-included models first
	PresetCustom                 // nothing pre-filled
)

// ModelOption is a model some usable provider offers.
type ModelOption struct {
	Provider string
	Model
}

func (o ModelOption) Spec() string { return o.Provider + "/" + o.ID }

// SuggestRouting fills roles and fallbacks for a preset from the models
// the user's providers offer. main is the current main model spec.
func SuggestRouting(p Preset, options []ModelOption, main string) config.Routing {
	r := config.Routing{Roles: map[string]string{}, Fallbacks: map[string][]string{}, Options: map[string]config.RoleOption{}}
	if p == PresetCustom {
		return r
	}
	var usable []ModelOption
	for _, o := range options {
		if o.Chat && (o.Tools || !o.CapsKnown) {
			usable = append(usable, o)
		}
	}
	if len(usable) == 0 {
		return r
	}
	mainCost := cheapness(ModelOption{Provider: providerOf(main), Model: Model{ID: modelOf(main)}})
	var prefer func(ModelOption) bool
	if p == PresetLocal {
		prefer = func(o ModelOption) bool { return isLocal(o.Provider) || o.Provider == Codex }
	}
	// byCost ranks the models, cheapest first; among equally cheap ones
	// (local models, say) bigger first, or smaller first when small wins.
	byCost := func(smallFirst bool) []ModelOption {
		out := slices.Clone(usable)
		slices.SortStableFunc(out, func(a, b ModelOption) int {
			pa, pb := prefer != nil && prefer(a), prefer != nil && prefer(b)
			if pa != pb {
				if pa {
					return -1
				}
				return 1
			}
			if c := cmp.Compare(cheapness(a), cheapness(b)); c != 0 {
				return c
			}
			// Equal cost (local, or one plan): the lighter model spares the
			// machine or the plan's quota.
			if c := cmp.Compare(nameTier(a.ID), nameTier(b.ID)); c != 0 {
				return c
			}
			c := cmp.Compare(paramsB(b.Params), paramsB(a.Params))
			if smallFirst {
				c = -c
			}
			if c != 0 {
				return c
			}
			return strings.Compare(a.Spec(), b.Spec())
		})
		return out
	}
	ranked := byCost(false)

	// Explore: the cheapest that can call tools, the smaller the faster.
	// Worker: the cheapest that isn't a tiny local model, which tends to
	// fumble multi-step edits. Neither is set if it would cost as much as
	// the main model.
	pick := func(ranked []ModelOption, skip func(ModelOption) bool) (ModelOption, bool) {
		for _, o := range ranked {
			if o.Spec() == main || skip(o) {
				continue
			}
			if p == PresetBalanced && cheapness(o) >= mainCost {
				continue
			}
			return o, true
		}
		return ModelOption{}, false
	}
	tooSmall := func(minB float64) func(ModelOption) bool {
		return func(o ModelOption) bool {
			return isLocal(o.Provider) && paramsB(o.Params) > 0 && paramsB(o.Params) < minB
		}
	}
	explore, okE := pick(byCost(true), tooSmall(3))
	worker, okW := pick(ranked, tooSmall(7))
	// Cheap models get less rope: the worker edits a worktree for the main
	// agent to review and merge, and both stop sooner.
	if okE {
		r.Roles[RoleExplore] = explore.Spec()
		r.Options[RoleExplore] = config.RoleOption{MaxTurns: DefaultExploreTurns, Context: "minimal"}
	}
	if okW {
		r.Roles[RoleWorker] = worker.Spec()
		r.Options[RoleWorker] = config.RoleOption{Isolation: "worktree", MaxTurns: DefaultWorkerTurns, Context: "minimal"}
	}
	// Keep the strong model reachable by name for hard subtasks, in case
	// the main model is later switched to a cheaper one.
	if p != PresetCheapest && main != "" && (okE || okW) {
		r.Roles[RoleSmart] = main
	}
	// Fall back to one model on another provider, so a rate limit or a
	// stopped local server doesn't stall the swarm.
	for role, chosen := range map[string]ModelOption{RoleWorker: worker, RoleExplore: explore} {
		if chosen.ID == "" {
			continue
		}
		for _, o := range ranked {
			if o.Provider != chosen.Provider && o.Spec() != main && !tooSmall(7)(o) {
				r.Fallbacks[role] = []string{o.Spec()}
				break
			}
		}
	}
	return r
}

// cheapness orders models by what a call costs the user, lowest first:
// local models, then subscription plans, then list price (or, without
// one, a guess from the name).
func cheapness(o ModelOption) float64 {
	switch {
	case isLocal(o.Provider):
		return 0
	case o.Provider == Codex:
		return 0.1 // included in a ChatGPT plan
	}
	if info := llm.Lookup(o.ID); info.InputPrice+info.OutputPrice > 0 {
		return info.InputPrice + info.OutputPrice*0.25 // agents read far more than they write
	}
	switch nameTier(o.ID) {
	case 0:
		return 1
	case 2:
		return 20
	}
	return 5
}

// nameTier guesses a model's weight class from its id: 0 light, 1
// unknown, 2 heavy.
func nameTier(id string) int {
	id = strings.ToLower(id)
	for _, s := range []string{"nano", "lite", "mini", "flash", "haiku", "luna", "small", "8b", "tiny"} {
		if strings.Contains(id, s) {
			return 0
		}
	}
	for _, s := range []string{"opus", "fable", "pro", "ultra", "large", "sol", "astra"} {
		if strings.Contains(id, s) {
			return 2
		}
	}
	return 1
}

func isLocal(provider string) bool {
	c, ok := ChoiceFor(provider)
	return ok && c.Local
}

// paramsB parses a parameter count such as "7.6B" into billions; 0 if unknown.
func paramsB(s string) float64 {
	s = strings.TrimSpace(strings.ToUpper(s))
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "B"):
		s = strings.TrimSuffix(s, "B")
	case strings.HasSuffix(s, "M"):
		s, mult = strings.TrimSuffix(s, "M"), 0.001
	default:
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f * mult
}

func providerOf(spec string) string {
	p, _, _ := strings.Cut(spec, "/")
	return p
}

func modelOf(spec string) string {
	_, m, ok := strings.Cut(spec, "/")
	if !ok {
		return spec
	}
	return m
}

// PriceNote is a model's list price per million input/output tokens, or
// "" when the catalog doesn't know it.
func PriceNote(spec string) string {
	info := llm.Lookup(modelOf(spec))
	if info.InputPrice == 0 && info.OutputPrice == 0 {
		return ""
	}
	return fmt.Sprintf("$%s/$%s per M", trimPrice(info.InputPrice), trimPrice(info.OutputPrice))
}

func trimPrice(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}
