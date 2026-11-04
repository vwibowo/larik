package desktop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/permission"
	"larik/internal/providers"
	"larik/internal/tools"
)

type settingsModelChoice struct {
	spec, description string
	model             providers.Model
}

type settingsState struct {
	page, query         string
	personal, effective *config.Config
	loadError           string
	errors              map[string]string
	modelDraft          string
	languageDraft       string
	browsing, loading   bool
	modelQuery          string
	modelChoices        []settingsModelChoice
	modelError          string
	listModels          func(context.Context, *config.Config) ([]settingsModelChoice, error)
}

func (a *App) refreshSettings() {
	if a.settings == nil {
		a.settings = &settingsState{page: "window", errors: map[string]string{}}
	}
	s := a.settings
	personal, err := config.LoadPersonal()
	if err != nil {
		s.loadError = err.Error()
		s.personal, s.effective = nil, nil
		return
	}
	dir := a.currentDir()
	if dir == "" || isRemote(dir) {
		dir = a.opts.Cwd
	}
	effective, err := config.Load(dir)
	if err != nil {
		s.loadError = err.Error()
		s.personal, s.effective = personal, nil
	} else {
		s.loadError = ""
		s.personal, s.effective = personal, effective
	}
	s.modelDraft = personal.Model
	s.languageDraft = personal.Language
}

func settingValue(cfg *config.Config, id string) string {
	if cfg == nil {
		return ""
	}
	switch id {
	case "model":
		if cfg.Model == "" {
			return "Automatic"
		}
		return cfg.Model
	case "effort":
		if cfg.Effort == "" {
			return "Provider default"
		}
		return string(cfg.Effort)
	case "mode":
		if cfg.Mode == "" {
			return "Ask before edits and commands"
		}
		return string(cfg.Mode)
	case "execution":
		e, _ := tools.ParseExecution(string(cfg.Execution))
		return string(e)
	case "language":
		if cfg.Language == "" {
			return "Model default"
		}
		return cfg.Language
	case "auto_compact":
		if cfg.AutoCompactOn() {
			return "On"
		}
		return "Off"
	case "token_saver":
		if cfg.TokenSaverOn() {
			return "On"
		}
		return "Off"
	case "prompt_suggestions":
		if cfg.PromptSuggestionsOn() {
			return "On"
		}
		return "Off"
	case "tool_search":
		if cfg.ToolSearch == "" {
			return "Automatic"
		}
		return cfg.ToolSearch
	}
	return ""
}

func (a *App) settingsDescription(id, fallback string) string {
	if a.settings == nil || a.settings.personal == nil {
		return fallback
	}
	cfg := a.settings.personal
	switch id {
	case "mode":
		switch cfg.Mode {
		case permission.ModeAcceptEdits:
			return "Edit project files without asking; ask before commands."
		case permission.ModePlan:
			return "Read files and propose changes without editing."
		case permission.ModeAuto:
			return "A model reviews actions that would otherwise need approval."
		case permission.ModeYolo:
			return "Run tools and commands without asking for approval."
		default:
			return "Ask before edits and commands."
		}
	case "execution":
		switch cfg.Execution {
		case tools.ExecTools:
			return "Offer direct tool calls to the model."
		case tools.ExecHybrid:
			return "Offer direct tools and code scripts; the model chooses."
		default:
			return "Use code scripts for ordinary tools and direct conversation tools."
		}
	case "tool_search":
		switch cfg.ToolSearch {
		case "on":
			return "Have the model search for MCP tools before using them."
		case "off":
			return "Send every MCP tool definition with each request."
		default:
			return "Load MCP tools on demand when their list grows large."
		}
	}
	return fallback
}

func (a *App) settingsValueNote(id string) string {
	if strings.HasPrefix(id, "integration-") {
		return a.integrationNote(id)
	}
	s := a.settings
	if s == nil || s.personal == nil {
		return ""
	}
	saved := settingValue(s.personal, id)
	if saved == "" {
		return ""
	}
	note := "Saved personal default: " + saved
	if effective := settingValue(s.effective, id); effective != "" && effective != saved {
		dir := a.currentDir()
		if dir == "" || isRemote(dir) {
			dir = a.opts.Cwd
		}
		note += " · Effective in " + filepath.Base(dir) + ": " + effective
	}
	return note
}

func (a *App) saveAgentSetting(id string, value any) {
	s := a.settings
	if s == nil || s.personal == nil {
		return
	}
	if err := s.personal.SetUserSettingPath(id, value); err != nil {
		s.errors[id] = "Could not save: " + err.Error()
		return
	}
	delete(s.errors, id)
	a.refreshSettings()
	a.settingsChanged()
}

func (a *App) saveModel(spec string) {
	s := a.settings
	if s.personal == nil {
		return
	}
	spec = strings.TrimSpace(spec)
	var value any
	var effort any
	if spec != "" {
		resolved, err := providers.Resolve(s.personal, spec)
		if err != nil {
			s.errors["model"] = "Could not use model: " + err.Error()
			return
		}
		spec = resolved.String()
		value = spec
		currentEffort := s.personal.Effort
		if currentEffort != "" {
			listed := a.modelsFor(resolved.Provider.Name())
			if !slices.Contains(providers.AvailableEfforts(s.personal, resolved.Provider.Name(), resolved.Model, listed), currentEffort) {
				effort = nil
			} else {
				effort = string(currentEffort)
			}
		}
	}
	updates := map[string]any{"model": value}
	if effort == nil {
		updates["effort"] = nil
	}
	if err := s.personal.SetUserSettings(updates); err != nil {
		s.errors["model"] = "Could not save: " + err.Error()
		return
	}
	delete(s.errors, "model")
	a.refreshSettings()
	a.settingsChanged()
}

func (a *App) modelsFor(provider string) []providers.Model {
	var models []providers.Model
	for _, choice := range a.settings.modelChoices {
		name, _, _ := strings.Cut(choice.spec, "/")
		if name == provider {
			models = append(models, choice.model)
		}
	}
	return models
}

func listSettingsModels(ctx context.Context, cfg *config.Config) ([]settingsModelChoice, error) {
	names := providers.Usable(cfg)
	if cfg.Model != "" {
		if r, err := providers.Resolve(cfg, cfg.Model); err == nil && !slices.Contains(names, r.Provider.Name()) {
			names = append(names, r.Provider.Name())
		}
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	var choices []settingsModelChoice
	var failures []error
	for _, name := range names {
		name := name
		wg.Go(func() {
			models, err := providers.EndpointFor(cfg, name).ListModels(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", name, err))
				return
			}
			for _, model := range models {
				choices = append(choices, settingsModelChoice{spec: name + "/" + model.ID, description: model.Desc, model: model})
			}
		})
	}
	wg.Wait()
	slices.SortFunc(choices, func(a, b settingsModelChoice) int { return strings.Compare(a.spec, b.spec) })
	if len(choices) == 0 && len(failures) > 0 {
		return choices, errors.Join(failures...)
	}
	return choices, nil
}

func (a *App) loadSettingsModels() {
	s := a.settings
	if s == nil || s.personal == nil || s.loading {
		return
	}
	s.browsing, s.loading, s.modelError = true, true, ""
	load := s.listModels
	if load == nil {
		load = listSettingsModels
	}
	cfg := s.effective
	if cfg == nil {
		cfg = s.personal
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		choices, err := load(ctx, cfg)
		apply := func() {
			s.modelChoices, s.loading = choices, false
			if err != nil {
				s.modelError = err.Error()
			}
		}
		if a.update != nil {
			a.update(apply)
		} else {
			apply()
		}
	}()
}

func effortLabel(e llm.Effort) string {
	switch e {
	case llm.EffortLow:
		return "Low"
	case llm.EffortMedium:
		return "Medium"
	case llm.EffortHigh:
		return "High"
	case llm.EffortXHigh:
		return "Extra high"
	case llm.EffortMax:
		return "Max"
	default:
		return "Provider default"
	}
}

func (a *App) allowedEfforts() []llm.Effort {
	s := a.settings
	if s == nil || s.personal == nil || s.personal.Model == "" {
		return []llm.Effort{llm.EffortDefault}
	}
	r, err := providers.Resolve(s.personal, s.personal.Model)
	if err != nil {
		return []llm.Effort{llm.EffortDefault}
	}
	return providers.AvailableEfforts(s.personal, r.Provider.Name(), r.Model, a.modelsFor(r.Provider.Name()))
}

func (a *App) agentSettingControl(c *ui.Context, t *ui.Theme, id string) {
	if strings.HasPrefix(id, "integration-") {
		a.integrationControl(c, id)
		return
	}
	s := a.settings
	if s.personal == nil && id != "connect" && id != "routing" && id != "permissions" &&
		id != "mcp" && id != "hooks" && id != "sandbox" && id != "config" {
		ui.Text(c, "Unavailable").TextColor(t.TextMuted)
		return
	}
	switch id {
	case "model":
		ui.Column(c).Width(245).Gap(6).Children(func() {
			ui.TextInput(c.Key("model-id"), &s.modelDraft).Placeholder("provider/model").Label("Default model ID").OnSubmit(func() { a.saveModel(s.modelDraft) }).FillWidth()
			ui.Row(c).Gap(6).Children(func() {
				if ui.Button(c.Key("save-model"), "Save model").Clicked() {
					a.saveModel(s.modelDraft)
				}
				if ui.Button(c.Key("browse-models"), "Browse models…").Clicked() {
					a.loadSettingsModels()
				}
			})
			if ui.Button(c.Key("connect-provider"), "Connect provider…").Clicked() {
				a.openSettingsCommand("/connect")
			}
			if s.browsing {
				if s.loading {
					ui.Text(c, "Loading models…").TextColor(t.TextMuted)
				}
				if s.modelError != "" {
					ui.Text(c, "Could not list models: "+s.modelError).FontSize(11).TextColor(t.Danger)
				}
				if !s.loading && len(s.modelChoices) == 0 {
					ui.Text(c, "Enter a model ID above, or connect a provider.").FontSize(11).TextColor(t.TextMuted)
				}
				if len(s.modelChoices) > 0 {
					ui.SearchField(c.Key("model-search"), &s.modelQuery).Placeholder("Filter models").Label("Filter models")
					ui.Scroll(c).Height(150).Children(func() {
						ui.Column(c).Gap(2).Children(func() {
							shown := 0
							for _, choice := range s.modelChoices {
								if !strings.Contains(strings.ToLower(choice.spec+" "+choice.description), strings.ToLower(s.modelQuery)) {
									continue
								}
								if shown == 100 {
									ui.Text(c, "Type to narrow the model list.").FontSize(11).TextColor(t.TextMuted)
									break
								}
								shown++
								choice := choice
								if ui.Button(c.Key(choice.spec), choice.spec).Label("Use " + choice.spec).Clicked() {
									a.saveModel(choice.spec)
								}
							}
						})
					})
				}
			}
		})
	case "effort":
		levels := a.allowedEfforts()
		options := make([]string, len(levels))
		selected := effortLabel(s.personal.Effort)
		for i, e := range levels {
			options[i] = effortLabel(e)
		}
		if !slices.Contains(options, selected) {
			selected = options[0]
		}
		if ui.Select(c.Key("effort-choice"), &selected, options).Width(170).Label("Choose reasoning effort").Changed() {
			for _, e := range levels {
				if effortLabel(e) == selected {
					if e == "" {
						a.saveAgentSetting("effort", nil)
					} else {
						a.saveAgentSetting("effort", string(e))
					}
					break
				}
			}
		}
	case "mode":
		labels := []string{"Ask each time", "Accept edits", "Plan only", "Auto approve", "Allow all"}
		values := []permission.Mode{permission.ModeDefault, permission.ModeAcceptEdits, permission.ModePlan, permission.ModeAuto, permission.ModeYolo}
		selected := labels[0]
		for i, v := range values {
			if s.personal.Mode == v {
				selected = labels[i]
			}
		}
		if ui.Select(c.Key("mode-choice"), &selected, labels).Width(170).Label("Permission mode").Changed() {
			for i, label := range labels {
				if label == selected {
					if i == 0 {
						a.saveAgentSetting("mode", nil)
					} else {
						a.saveAgentSetting("mode", string(values[i]))
					}
				}
			}
		}
	case "execution":
		labels := []string{"Code scripts", "Hybrid", "Direct tools"}
		values := []tools.Execution{tools.ExecCode, tools.ExecHybrid, tools.ExecTools}
		selected := labels[0]
		for i, v := range values {
			if s.personal.Execution == v {
				selected = labels[i]
			}
		}
		if ui.Select(c.Key("execution-choice"), &selected, labels).Width(170).Label("Execution style").Changed() {
			for i, label := range labels {
				if label == selected {
					if i == 0 {
						a.saveAgentSetting("execution", nil)
					} else {
						a.saveAgentSetting("execution", string(values[i]))
					}
				}
			}
		}
	case "language":
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c.Key("language-input"), &s.languageDraft).Width(145).Placeholder("Model default").Label("Response language").OnSubmit(func() { a.saveAgentSetting("language", strings.TrimSpace(s.languageDraft)) })
			if ui.Button(c.Key("save-language"), "Save").Clicked() {
				a.saveAgentSetting("language", strings.TrimSpace(s.languageDraft))
			}
		})
	case "auto_compact", "token_saver", "prompt_suggestions":
		on := false
		switch id {
		case "auto_compact":
			on = s.personal.AutoCompactOn()
		case "token_saver":
			on = s.personal.TokenSaverOn()
		case "prompt_suggestions":
			on = s.personal.PromptSuggestionsOn()
		}
		if ui.Switch(c.Key("switch"), &on).Label(id).Changed() {
			if (id == "auto_compact" || id == "prompt_suggestions") && on || id == "token_saver" && !on {
				a.saveAgentSetting(id, nil)
			} else {
				a.saveAgentSetting(id, on)
			}
		}
	case "tool_search":
		labels := []string{"Automatic", "Always", "Never"}
		values := []string{"", "on", "off"}
		selected := labels[0]
		for i, v := range values {
			if s.personal.ToolSearch == v {
				selected = labels[i]
			}
		}
		if ui.Select(c.Key("tool-search-choice"), &selected, labels).Width(160).Label("Load MCP tools on demand").Changed() {
			for i, label := range labels {
				if label == selected {
					if i == 0 {
						a.saveAgentSetting("tool_search", nil)
					} else {
						a.saveAgentSetting("tool_search", values[i])
					}
				}
			}
		}
	default:
		commands := map[string]string{
			"connect": "/connect", "routing": "/routing", "permissions": "/config permissions",
			"mcp": "/config mcp", "hooks": "/config hooks", "sandbox": "/config sandbox", "config": "/config",
		}
		if command := commands[id]; command != "" && ui.Button(c.Key("open"), "Open…").Label("Open "+id+" settings").Clicked() {
			a.openSettingsCommand(command)
		}
	}
}

func (a *App) openSettingsCommand(command string) {
	if t := a.startTab(a.currentDir(), "", "", command); t != nil {
		a.activate(t)
	}
	if a.raise != nil {
		a.raise()
	}
	a.settingsChanged()
}
