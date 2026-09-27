package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/providers"
)

func routingModel(t *testing.T) *model {
	t.Helper()
	m := testModel(t) // main model ollama/m
	m.modelLists = map[string]providerModels{
		"anthropic": {models: []providers.Model{{ID: "claude-opus-5", Chat: true}, {ID: "claude-haiku-4-5", Chat: true}}},
		"ollama":    {models: []providers.Model{{ID: "qwen3-coder", Params: "30.5B", Chat: true}}},
	}
	return m
}

func hit(m *model, k rune) { m.Update(press(k)) }

func TestRoutingWizardSavesPresetAndEdits(t *testing.T) {
	m := routingModel(t)
	m.command("/routing")
	if m.routing == nil || m.routing.step != rtPreset {
		t.Fatal("wizard didn't open on the preset step")
	}
	hit(m, tea.KeyDown) // Cheapest
	hit(m, tea.KeyEnter)
	w := m.routing
	if w.step != rtRoles || w.r.Roles["worker"] != "ollama/qwen3-coder" {
		t.Fatalf("after preset: step %d roles %v", w.step, w.r.Roles)
	}

	// smart is the first row: pick Opus for it by typing a filter.
	hit(m, tea.KeyEnter)
	if w.models == nil {
		t.Fatal("enter should open the model list")
	}
	typeText(m, "opus")
	hit(m, tea.KeyEnter)
	if w.r.Roles["smart"] != "anthropic/claude-opus-5" {
		t.Fatalf("smart = %q", w.r.Roles["smart"])
	}
	view := plain(w.view(m.st, 100, 40))
	if !strings.Contains(view, "anthropic/claude-opus-5  $5/$25 per M") {
		t.Errorf("roles view lacks the price:\n%s", view)
	}

	// The preset gives the worker a worktree and a turn cap; w and +/-
	// change them.
	if o := w.r.Options["worker"]; o.Isolation != "worktree" || o.MaxTurns != 40 {
		t.Fatalf("worker options = %+v", o)
	}
	hit(m, tea.KeyDown) // worker
	typeText(m, "w")
	typeText(m, "-")
	if o := w.r.Options["worker"]; o.Isolation != "" || o.MaxTurns != 30 {
		t.Errorf("after w and -: %+v", o)
	}
	typeText(m, "w")
	if !strings.Contains(plain(w.view(m.st, 120, 40)), "[worktree · 30 turns]") {
		t.Errorf("roles view lacks the options:\n%s", plain(w.view(m.st, 120, 40)))
	}

	typeText(m, "n") // to fallbacks
	if w.step != rtFallbacks || w.r.Fallbacks["worker"][0] != "anthropic/claude-haiku-4-5" {
		t.Fatalf("fallbacks step %d: %v", w.step, w.r.Fallbacks)
	}
	typeText(m, "n") // to budget
	typeText(m, "1.5")
	hit(m, tea.KeyEnter)
	if w.step != rtSave || w.r.Budget.SessionUSD != 1.5 {
		t.Fatalf("budget step: %d %+v err %q", w.step, w.r.Budget, w.err)
	}
	hit(m, tea.KeyDown) // this project only
	hit(m, tea.KeyEnter)
	if m.routing != nil {
		t.Fatalf("wizard still open: %q", w.err)
	}

	data, err := os.ReadFile(config.LocalSettingsPath(m.opts.Config.Cwd))
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Roles       map[string]string            `json:"roles"`
		Fallbacks   map[string][]string          `json:"fallbacks"`
		Budget      config.Budget                `json:"budget"`
		RoleOptions map[string]config.RoleOption `json:"role_options"`
	}
	json.Unmarshal(data, &saved)
	if saved.Roles["smart"] != "anthropic/claude-opus-5" || saved.Roles["worker"] != "ollama/qwen3-coder" ||
		saved.Fallbacks["worker"][0] != "anthropic/claude-haiku-4-5" || saved.Budget.SessionUSD != 1.5 ||
		saved.RoleOptions["worker"] != (config.RoleOption{Isolation: "worktree", MaxTurns: 30}) {
		t.Errorf("saved %s", data)
	}
	if m.opts.Config.Routing().Roles["smart"] == "" {
		t.Errorf("the running config wasn't updated")
	}
}

func TestRoutingWizardCancelWritesNothing(t *testing.T) {
	m := routingModel(t)
	m.command("/routing")
	hit(m, tea.KeyEnter) // Balanced
	hit(m, tea.KeyEscape)
	hit(m, tea.KeyEscape)
	if m.routing != nil {
		t.Fatal("esc on the first step should close the wizard")
	}
	if _, err := os.Stat(m.opts.Config.UserConfigPath()); err == nil {
		t.Errorf("cancelling wrote the config")
	}
}

func TestRoutingWizardRejectsBadBudget(t *testing.T) {
	m := routingModel(t)
	m.openRouting(rtBudget)
	typeText(m, "lots")
	hit(m, tea.KeyEnter)
	if m.routing.step != rtBudget || !strings.Contains(m.routing.err, "number of dollars") {
		t.Errorf("step %d err %q", m.routing.step, m.routing.err)
	}
}

func TestRoutingCommandKeyValue(t *testing.T) {
	m := routingModel(t)
	m.command("/routing worker=anthropic/claude-haiku-4-5 budget=$3")
	r := m.opts.Config.Routing()
	if r.Roles["worker"] != "anthropic/claude-haiku-4-5" || r.Budget.SessionUSD != 3 {
		t.Fatalf("routing = %+v", r)
	}
	if !strings.Contains(savedConfig(t, m), `"worker": "anthropic/claude-haiku-4-5"`) {
		t.Errorf("not saved: %s", savedConfig(t, m))
	}
	m.command("/routing worker.isolation=worktree worker.max_turns=25")
	if o := m.opts.Config.Routing().Options["worker"]; o.Isolation != "worktree" || o.MaxTurns != 25 {
		t.Errorf("options = %+v", o)
	}
	m.command("/routing worker.isolation=docker")
	if m.opts.Config.Routing().Options["worker"].Isolation != "worktree" {
		t.Errorf("a bad isolation value was saved")
	}
	m.command("/routing explore=worker")
	if m.opts.Config.Routing().Roles["explore"] != "" {
		t.Errorf("a role naming a role was accepted")
	}
	m.command("/routing worker=")
	if m.opts.Config.Routing().Roles["worker"] != "" {
		t.Errorf("worker= should clear the role")
	}
}

func TestConfigShowsRoutingRows(t *testing.T) {
	m := routingModel(t)
	m.command("/routing worker=anthropic/claude-haiku-4-5")
	m.command("/config")
	selectSetting(t, m, "routing")
	if it, _ := m.settings.list.selected(); it.detail != "worker anthropic/claude-haiku-4-5" || it.note != "/routing" {
		t.Errorf("routing row = %+v", it)
	}
	hit(m, tea.KeyEnter)
	if m.routing == nil {
		t.Errorf("enter on the routing row should open the wizard")
	}
}
