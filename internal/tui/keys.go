package tui

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"

	"larik/internal/config"
)

// Actions the keybindings setting can rebind. Keys inside panels
// (pickers, permission prompts, the wizard) aren't rebindable, and ctrl+c
// always interrupts, clears or quits, so there is always a way out.
const (
	actSubmit         = "submit"
	actNewline        = "newline"
	actInterrupt      = "interrupt"
	actQuit           = "quit"
	actHistorySearch  = "history_search"
	actExternalEditor = "external_editor"
	actPasteImage     = "paste_image"
	actCycleMode      = "cycle_mode"
	actToggleThinking = "toggle_thinking"
	actModelPicker    = "model_picker"
	actShortcuts      = "shortcuts"
	actScrollUp       = "scroll_up"
	actScrollDown     = "scroll_down"
	actScrollTop      = "scroll_top"
	actScrollBottom   = "scroll_bottom"
)

// defaultKeys are the bindings before the setting applies, in the order
// the actions are listed to the user.
var defaultKeys = []struct {
	action string
	keys   []string
}{
	{actSubmit, []string{"enter"}},
	{actNewline, []string{"shift+enter", "alt+enter", "ctrl+j"}},
	{actInterrupt, []string{"esc"}},
	{actQuit, []string{"ctrl+d"}},
	{actHistorySearch, []string{"ctrl+r"}},
	{actExternalEditor, []string{"ctrl+g"}},
	{actPasteImage, []string{"ctrl+v"}},
	{actCycleMode, []string{"shift+tab"}},
	{actToggleThinking, []string{"ctrl+o"}},
	{actModelPicker, []string{"alt+p"}},
	{actShortcuts, []string{"?"}},
	{actScrollUp, []string{"pgup"}},
	{actScrollDown, []string{"pgdown"}},
	{actScrollTop, []string{"ctrl+home"}},
	{actScrollBottom, []string{"ctrl+end"}},
}

// keymap maps keys to actions and back.
type keymap struct {
	action map[string]string   // key → action
	keys   map[string][]string // action → keys, in order
}

// newKeymap applies the keybindings setting to the defaults. A key the
// setting gives an action is taken from whichever action had it by
// default. Entries that can't apply are skipped and described in the
// returned warnings.
func newKeymap(bindings map[string]config.KeyList) (keymap, []string) {
	km := keymap{action: map[string]string{}, keys: map[string][]string{}}
	known := map[string]bool{}
	for _, d := range defaultKeys {
		known[d.action] = true
	}
	var warnings []string
	custom := map[string][]string{} // valid entries of the setting
	claimed := map[string]string{}  // key → the action the setting gives it
	actions := make([]string, 0, len(bindings))
	for a := range bindings {
		actions = append(actions, a)
	}
	sort.Strings(actions)
	for _, a := range actions {
		if !known[a] {
			warnings = append(warnings, fmt.Sprintf("keybindings: unknown action %q", a))
			continue
		}
		var keys []string
		for _, k := range bindings[a] {
			k = strings.ToLower(strings.TrimSpace(k))
			if err := checkKey(a, k); err != nil {
				warnings = append(warnings, fmt.Sprintf("keybindings.%s: %v", a, err))
				continue
			}
			if other, ok := claimed[k]; ok {
				warnings = append(warnings, fmt.Sprintf("keybindings.%s: %s is already bound to %s", a, k, other))
				continue
			}
			claimed[k] = a
			keys = append(keys, k)
		}
		custom[a] = keys
	}
	for _, d := range defaultKeys {
		keys, ok := custom[d.action]
		if !ok {
			for _, k := range d.keys {
				if _, taken := claimed[k]; !taken {
					keys = append(keys, k)
				}
			}
		}
		km.keys[d.action] = keys
		for _, k := range keys {
			km.action[k] = d.action
		}
	}
	return km, warnings
}

// keyNames are the named keys a binding may use besides single
// characters.
var keyNames = map[string]bool{
	"enter": true, "tab": true, "esc": true, "space": true, "backspace": true,
	"delete": true, "insert": true, "up": true, "down": true, "left": true,
	"right": true, "home": true, "end": true, "pgup": true, "pgdown": true,
}

// checkKey reports whether k, as Bubble Tea names keys ("ctrl+shift+x"),
// can be bound to action.
func checkKey(action, k string) error {
	if k == "" {
		return fmt.Errorf("empty key")
	}
	parts := strings.Split(k, "+")
	base, mods := parts[len(parts)-1], parts[:len(parts)-1]
	for _, m := range mods {
		if !slices.Contains([]string{"ctrl", "alt", "shift", "super", "meta", "hyper"}, m) {
			return fmt.Errorf("%q: unknown modifier %q", k, m)
		}
	}
	isFn := len(base) >= 2 && base[0] == 'f' && strings.Trim(base[1:], "0123456789") == ""
	if !keyNames[base] && !isFn && utf8.RuneCountInString(base) != 1 {
		return fmt.Errorf("%q: unknown key %q", k, base)
	}
	if k == "ctrl+c" {
		return fmt.Errorf("ctrl+c can't be rebound; it always interrupts, clears or quits")
	}
	// A bare character would stop you typing it. "?" is the exception
	// for the shortcuts, which only open on an empty prompt.
	if len(mods) == 0 && utf8.RuneCountInString(base) == 1 && action != actShortcuts {
		return fmt.Errorf("%q: a character without a modifier would stop you typing it", k)
	}
	return nil
}

// is reports whether k is bound to action.
func (km keymap) is(k, action string) bool { return km.action[k] == action }

// hint is the first key bound to action, for hints in the interface;
// empty when the action is unbound.
func (km keymap) hint(action string) string {
	if keys := km.keys[action]; len(keys) > 0 {
		return keys[0]
	}
	return ""
}

// all lists every key bound to action, for the shortcuts overlay.
func (km keymap) all(action string) string {
	if keys := km.keys[action]; len(keys) > 0 {
		return strings.Join(keys, ", ")
	}
	return "unbound"
}

// binding is the keys of action as a bubbles binding, disabled when the
// action is unbound.
func (km keymap) binding(action string) key.Binding {
	b := key.NewBinding(key.WithKeys(km.keys[action]...))
	b.SetEnabled(len(km.keys[action]) > 0)
	return b
}

// keyHint joins a hint's key with its text ("ctrl+o to show"), or
// returns "" when the action is unbound so the hint drops out.
func (m *model) keyHint(action, text string) string {
	if k := m.keys.hint(action); k != "" {
		return k + " " + text
	}
	return ""
}

// defaultAction is the action a key is bound to by default, if any.
func defaultAction(k string) (string, bool) {
	for _, d := range defaultKeys {
		if slices.Contains(d.keys, k) {
			return d.action, true
		}
	}
	return "", false
}

// remap turns a default key named in help text into the key now bound
// to its action: "" when the action is unbound, k itself when k isn't
// a rebindable key.
func (km keymap) remap(k string) string {
	if a, ok := defaultAction(k); ok {
		return km.hint(a)
	}
	return k
}

// interruptHint is " (esc to interrupt)", for messages about a busy agent.
func (m *model) interruptHint() string {
	return paren(m.keyHint(actInterrupt, "to interrupt"))
}

// paren is " (s)", or "" for an empty s.
func paren(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

// thinkingHint is " · ctrl+o to show" after a collapsed thought.
func (m *model) thinkingHint() string {
	if h := m.keyHint(actToggleThinking, "to show"); h != "" {
		return m.st.dim.Render(" · " + h)
	}
	return ""
}

// appendHint appends a hint unless it's empty (its action is unbound).
func appendHint(hints []string, h string) []string {
	if h == "" {
		return hints
	}
	return append(hints, h)
}
