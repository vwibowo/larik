package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/permission"
)

type ruleRef struct {
	deny  bool
	index int
	add   bool
}

type permissionsEditor struct {
	scope   int // 0: personal defaults, 1: private settings for this project
	user    permission.Rules
	project permission.Rules
	list    *picker
	input   *textinput.Model
	editing ruleRef
	dirty   [2]bool
	status  string
	failed  bool
	closing bool
}

func cloneRules(r permission.Rules) permission.Rules {
	return permission.Rules{
		Allow: append([]string(nil), r.Allow...),
		Deny:  append([]string(nil), r.Deny...),
	}
}

func newPermissionsEditor(cfg *config.Config) *permissionsEditor {
	e := &permissionsEditor{}
	var errs []string
	if r, err := config.PermissionRulesAt(cfg.UserConfigPath()); err != nil {
		errs = append(errs, shortHome(cfg.UserConfigPath())+": "+err.Error())
	} else {
		e.user = cloneRules(r)
	}
	projectPath := config.LocalSettingsPath(cfg.Cwd)
	if r, err := config.PermissionRulesAt(projectPath); err != nil {
		errs = append(errs, shortHome(projectPath)+": "+err.Error())
	} else {
		e.project = cloneRules(r)
	}
	if len(errs) > 0 {
		e.status, e.failed = strings.Join(errs, " · "), true
	}
	e.rebuild()
	return e
}

func (e *permissionsEditor) rules() *permission.Rules {
	if e.scope == 1 {
		return &e.project
	}
	return &e.user
}

func (e *permissionsEditor) rebuild() {
	p := &picker{}
	r := e.rules()
	for i, rule := range r.Allow {
		p.items = append(p.items, pickItem{section: "allow", label: rule, detail: "runs without asking", value: ruleRef{index: i}})
	}
	p.items = append(p.items, pickItem{section: "allow", label: "+ Add allow rule…", detail: "personal scopes only", value: ruleRef{add: true}})
	for i, rule := range r.Deny {
		p.items = append(p.items, pickItem{section: "deny (wins over allow)", label: rule, detail: "blocked", warn: true, value: ruleRef{deny: true, index: i}})
	}
	p.items = append(p.items, pickItem{section: "deny (wins over allow)", label: "+ Add deny rule…", detail: "blocks matching tool calls", value: ruleRef{deny: true, add: true}})
	p.home()
	if e.list != nil {
		if old, ok := e.list.selected(); ok {
			ref, _ := old.value.(ruleRef)
			p.selectWhere(func(it pickItem) bool { return it.value == ref })
		}
	}
	e.list = p
}

func (m *model) openPermissions() tea.Cmd {
	m.settings = &settingsPanel{rules: newPermissionsEditor(m.opts.Config)}
	return nil
}

func (e *permissionsEditor) startEdit(ref ruleRef, width int) tea.Cmd {
	ti := textinput.New()
	ti.Prompt = "› "
	ti.Placeholder = "tool or tool(pattern), e.g. bash(go test*)"
	ti.SetWidth(max(width-10, 20))
	if !ref.add {
		r := e.rules()
		if ref.deny {
			ti.SetValue(r.Deny[ref.index])
		} else {
			ti.SetValue(r.Allow[ref.index])
		}
	}
	e.editing, e.input, e.status = ref, &ti, ""
	return ti.Focus()
}

func (e *permissionsEditor) finishEdit() {
	rule := strings.TrimSpace(e.input.Value())
	if err := permission.ValidateRule(rule); err != nil {
		e.status, e.failed = err.Error(), true
		return
	}
	r := e.rules()
	ref := e.editing
	if ref.deny {
		if ref.add {
			r.Deny = append(r.Deny, rule)
		} else {
			r.Deny[ref.index] = rule
		}
	} else if ref.add {
		r.Allow = append(r.Allow, rule)
	} else {
		r.Allow[ref.index] = rule
	}
	e.dirty[e.scope] = true
	e.input, e.status, e.failed = nil, "Not saved yet", false
	e.rebuild()
}

func (e *permissionsEditor) removeSelected() {
	it, ok := e.list.selected()
	if !ok {
		return
	}
	ref, ok := it.value.(ruleRef)
	if !ok || ref.add {
		return
	}
	r := e.rules()
	if ref.deny {
		r.Deny = append(r.Deny[:ref.index], r.Deny[ref.index+1:]...)
	} else {
		r.Allow = append(r.Allow[:ref.index], r.Allow[ref.index+1:]...)
	}
	e.dirty[e.scope] = true
	e.status, e.failed = "Removed · not saved yet", false
	e.rebuild()
}

func (m *model) savePermissions() tea.Cmd {
	e := m.settings.rules
	rules := cloneRules(*e.rules())
	var value any = rules
	if len(rules.Allow) == 0 && len(rules.Deny) == 0 {
		value = nil
	}
	var path string
	var err error
	if e.scope == 1 {
		path = config.LocalSettingsPath(m.opts.Config.Cwd)
		err = m.opts.Config.SetProjectSettingPath("permissions", value)
	} else {
		path = m.opts.Config.UserConfigPath()
		err = m.opts.Config.SetUserSettingPath("permissions", value)
	}
	if err != nil {
		e.status, e.failed = "Couldn't save: "+err.Error(), true
		return nil
	}
	effective, err := m.opts.Config.EffectivePermissionRules()
	if err != nil {
		e.status, e.failed = "Saved, but couldn't apply: "+err.Error(), true
		return nil
	}
	m.opts.Config.Permissions = effective
	m.agent.Perms().SetRules(effective)
	e.dirty[e.scope], e.closing = false, false
	e.status, e.failed = "Saved to "+shortHome(path)+" · active now", false
	return nil
}

func (m *model) handlePermissionsKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.settings.rules
	k := msg.String()
	if e.input != nil {
		switch k {
		case "esc", "ctrl+c":
			e.input, e.status = nil, ""
			return nil
		case "enter":
			e.finishEdit()
			return nil
		}
		ti, cmd := e.input.Update(msg)
		e.input = &ti
		return cmd
	}
	switch k {
	case "ctrl+c":
		m.settings = nil
		return nil
	case "esc":
		if e.dirty[e.scope] && !e.closing {
			e.status, e.failed, e.closing = "Unsaved changes · s save · esc discard", true, true
			return nil
		}
		m.settings = nil
		return nil
	case "tab":
		if e.dirty[e.scope] {
			e.status, e.failed = "Save or discard changes before switching scope", true
			return nil
		}
		e.scope = 1 - e.scope
		e.status, e.failed, e.closing = "", false, false
		e.rebuild()
		return nil
	case "s", "ctrl+s":
		return m.savePermissions()
	case "d", "backspace", "delete":
		e.removeSelected()
		return nil
	}
	if e.list.handleKey(msg) {
		it, _ := e.list.selected()
		return e.startEdit(it.value.(ruleRef), m.width)
	}
	return nil
}

func (m *model) permissionsView() string {
	e := m.settings.rules
	w := max(m.width-6, 20)
	rows := m.availablePanelRows()
	scope := "all projects · personal"
	path := m.opts.Config.UserConfigPath()
	if e.scope == 1 {
		scope = "this project · private"
		path = config.LocalSettingsPath(m.opts.Config.Cwd)
	}
	head := spread(m.st.accent.Render("Permission rules"), m.st.dim.Render(scope), w)
	var body, hint string
	if e.input != nil {
		kind := "Allow rule"
		if e.editing.deny {
			kind = "Deny rule"
		}
		body = m.st.dim.Render(kind+" · "+shortHome(path)) + "\n" + e.input.View()
		hint = "enter apply to draft · esc cancel"
	} else {
		e.list.height = max(rows-5-boolRows(e.status != ""), 1)
		body = e.list.view(m.st, w)
		hint = "↑/↓ move · enter add/edit · d delete · s save · tab scope · esc close"
	}
	out := head + "\n" + body
	if e.status != "" {
		style := m.st.ok
		if e.failed {
			style = m.st.err
		}
		out += "\n" + style.Render(e.status)
	}
	return m.st.modal.Width(max(m.width-2, 10)).Render(out + "\n" + m.st.dim.Render(hint))
}

func permissionSummary(r permission.Rules) string {
	return fmt.Sprintf("%d allow · %d deny", len(r.Allow), len(r.Deny))
}
