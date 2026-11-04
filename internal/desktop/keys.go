package desktop

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// Keys: Settings › Keyboard gives the menu bar's commands keys of your
// own, or none. A key is written as the menus show them, its modifiers
// and then the key, "Ctrl+Cmd+K"; the one given replaces the command's
// own, in the menus, the palette and the window's keys.

// keyMods are the modifiers a key is written with, in the order it's
// written, and the names they go by.
var keyMods = []struct {
	name  string
	mod   ui.Modifiers
	alias []string
}{
	{"Ctrl", ui.Ctrl, []string{"ctrl", "control", "⌃"}},
	{"Alt", ui.Alt, []string{"alt", "option", "opt", "⌥"}},
	{"Shift", ui.Shift, []string{"shift", "⇧"}},
	{"Cmd", ui.Super, []string{"cmd", "command", "super", "⌘"}},
}

// keyNames are the keys other than letters, digits and F1 to F12, by the
// names a key is written with, the first of each its own.
var keyNames = []struct {
	key   ui.Key
	names []string
}{
	{ui.KeyLeft, []string{"Left", "←"}}, {ui.KeyRight, []string{"Right", "→"}},
	{ui.KeyUp, []string{"Up", "↑"}}, {ui.KeyDown, []string{"Down", "↓"}},
	{ui.KeyEnter, []string{"Enter", "Return", "↩"}}, {ui.KeyTab, []string{"Tab"}},
	{ui.KeySpace, []string{"Space"}}, {ui.KeyEscape, []string{"Escape", "Esc"}},
	{ui.KeyBackspace, []string{"Backspace", "Delete"}}, {ui.KeyDelete, []string{"ForwardDelete"}},
	{ui.KeyHome, []string{"Home"}}, {ui.KeyEnd, []string{"End"}},
	{ui.KeyPageUp, []string{"PageUp"}}, {ui.KeyPageDown, []string{"PageDown"}},
	{ui.KeyEqual, []string{"="}}, {ui.KeyMinus, []string{"-"}}, {ui.KeyComma, []string{","}},
	{ui.KeyPeriod, []string{"."}}, {ui.KeySlash, []string{"/"}}, {ui.KeySemicolon, []string{";"}},
	{ui.KeyQuote, []string{"'"}}, {ui.KeyBracketLeft, []string{"["}}, {ui.KeyBracketRight, []string{"]"}},
	{ui.KeyBackslash, []string{"\\"}}, {ui.KeyBackquote, []string{"`"}},
}

// parseKey reads a key as written, "Ctrl+Cmd+K", in any case, with the
// modifiers in any order, and returns it as the menus write it. A key
// takes a modifier, but for F1 to F12, so that typing isn't taken.
func parseKey(s string) (mods ui.Modifiers, key ui.Key, canon string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, "", fmt.Errorf("no key")
	}
	parts := strings.Split(s, "+")
	if strings.HasSuffix(s, "++") || s == "+" { // the plus key isn't one
		return 0, 0, "", fmt.Errorf("%q: use = for the plus key", s)
	}
	name := strings.TrimSpace(parts[len(parts)-1])
	for _, p := range parts[:len(parts)-1] {
		p = strings.ToLower(strings.TrimSpace(p))
		i := slices.IndexFunc(keyMods, func(m struct {
			name  string
			mod   ui.Modifiers
			alias []string
		}) bool {
			return slices.Contains(m.alias, p)
		})
		if i < 0 {
			return 0, 0, "", fmt.Errorf("%q isn't a modifier: Ctrl, Alt, Shift or Cmd", p)
		}
		mods |= keyMods[i].mod
	}
	key, name = keyOf(name)
	if key == ui.KeyUnknown {
		return 0, 0, "", fmt.Errorf("%q isn't a key", parts[len(parts)-1])
	}
	if mods&^ui.Shift == 0 && !(key >= ui.KeyF1 && key <= ui.KeyF12) {
		return 0, 0, "", fmt.Errorf("a key needs Ctrl, Alt or Cmd, so typing still types")
	}
	var b strings.Builder
	for _, m := range keyMods {
		if mods&m.mod != 0 {
			b.WriteString(m.name + "+")
		}
	}
	return mods, key, b.String() + name, nil
}

// keyOf is the key a name says, and the name it goes by.
func keyOf(name string) (ui.Key, string) {
	if r := []rune(name); len(r) == 1 {
		switch c := r[0]; {
		case c >= 'a' && c <= 'z':
			return ui.KeyA + ui.Key(c-'a'), strings.ToUpper(name)
		case c >= 'A' && c <= 'Z':
			return ui.KeyA + ui.Key(c-'A'), name
		case c >= '0' && c <= '9':
			return ui.Key0 + ui.Key(c-'0'), name
		}
	}
	var n int
	if _, err := fmt.Sscanf(strings.ToUpper(name), "F%d", &n); err == nil && n >= 1 && n <= 12 && strings.EqualFold(name, fmt.Sprintf("F%d", n)) {
		return ui.KeyF1 + ui.Key(n-1), fmt.Sprintf("F%d", n)
	}
	for _, k := range keyNames {
		for _, s := range k.names {
			if strings.EqualFold(s, name) {
				return k.key, k.names[0]
			}
		}
	}
	return ui.KeyUnknown, ""
}

// rebindable are the commands Settings › Keyboard lists, by menu: the
// menu bar's own, without the open tabs and sessions the Window menu
// and the palette list.
func (a *App) rebindable() []menuGroup {
	var out []menuGroup
	tabs := len(a.tabCommands())
	for _, g := range a.menuDefaults() {
		cmds := g.items
		if g.title == "Window" {
			cmds = cmds[:len(cmds)-tabs]
		}
		var keep []command
		for _, cmd := range cmds {
			if cmd.run != nil && cmd.label != "" {
				keep = append(keep, cmd)
			}
		}
		if len(keep) > 0 {
			out = append(out, menuGroup{title: cmp.Or(g.title, "Larik"), items: keep})
		}
	}
	return out
}

// bindKeys gives the commands of groups the keys Settings gave them.
func (a *App) bindKeys(groups []menuGroup) []menuGroup {
	if len(a.keys) == 0 {
		return groups
	}
	for _, g := range groups {
		for i, cmd := range g.items {
			if k, ok := a.keys[cmd.label]; ok && cmd.run != nil && cmd.label != "" {
				g.items[i].key = k
			}
		}
	}
	return groups
}

// setKey gives the command label the key written as s, or none for "",
// unless another command has it. It returns why not.
func (a *App) setKey(label, s string) error {
	canon := ""
	if strings.TrimSpace(s) != "" {
		var err error
		if _, _, canon, err = parseKey(s); err != nil {
			return err
		}
		for _, g := range a.menuBar() {
			for _, cmd := range g.items {
				if cmd.label != label && cmd.run != nil && cmd.key != "" && sameKey(cmd.key, canon) {
					return fmt.Errorf("%s already has %s", cmd.label, keyText(canon))
				}
			}
		}
	}
	if a.keys == nil {
		a.keys = map[string]string{}
	}
	if def := a.defaultKey(label); canon == def {
		delete(a.keys, label)
	} else {
		a.keys[label] = canon
	}
	a.settingsChanged()
	return nil
}

// resetKey gives the command label its own key back.
func (a *App) resetKey(label string) {
	delete(a.keys, label)
	a.settingsChanged()
}

// defaultKey is the command label's own key, "" for none.
func (a *App) defaultKey(label string) string {
	for _, g := range a.menuDefaults() {
		for _, cmd := range g.items {
			if cmd.label == label {
				return cmd.key
			}
		}
	}
	return ""
}

// sameKey reports whether two keys, as written, are one.
func sameKey(x, y string) bool {
	_, _, cx, ex := parseKey(x)
	_, _, cy, ey := parseKey(y)
	return ex == nil && ey == nil && cx == cy
}

// keyed reports whether the window's own key for the command label is
// the one pressed: it isn't, when Settings gave the command another.
func (a *App) keyed(c *ui.Context, label string, mods ui.Modifiers, key ui.Key) bool {
	if _, ok := a.keys[label]; ok {
		return false
	}
	return c.Shortcut(mods, key)
}

// customShortcuts runs the commands whose keys Settings gave them, when
// theirs is pressed.
func (a *App) customShortcuts(c *ui.Context) {
	if len(a.keys) == 0 {
		return
	}
	for _, g := range a.menuBar() {
		for _, cmd := range g.items {
			k, ok := a.keys[cmd.label]
			if !ok || k == "" || cmd.run == nil {
				continue
			}
			if mods, key, _, err := parseKey(k); err == nil && c.Shortcut(mods, key) {
				cmd.run()
				return
			}
		}
	}
}

// keySettings is Settings' Keyboard section: each command with its key,
// to type another into; Enter gives it, and an empty one takes it away.
func (a *App) keySettings(c *ui.Context, t *ui.Theme) {
	ui.Column(c.Key("keys")).Gap(6).Children(func() {
		ui.Text(c, "Keyboard").Bold().FontSize(13)
		ui.Text(c, "Type a key, as Ctrl+Cmd+K, and press Enter; leave it empty for none.").FontSize(12).TextColor(t.TextMuted)
		if a.keyError != "" {
			ui.Text(c, a.keyError).FontSize(12).TextColor(t.Danger)
		}
		if a.keyDrafts == nil {
			a.keyDrafts = map[string]*string{}
		}
		for _, g := range a.rebindable() {
			ui.Text(c, g.title).FontSize(11).Bold().TextColor(t.TextMuted).Padding(6, 0, 0, 0)
			for _, cmd := range a.bindKeys([]menuGroup{g})[0].items {
				label := cmd.label
				draft := a.keyDrafts[label]
				if draft == nil {
					k := cmd.key
					draft = &k
					a.keyDrafts[label] = draft
				}
				ui.Row(c.Key(label)).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, label).FontSize(12).Grow(1).Shrink(1).SingleLine()
					ui.TextInput(c.Key("key"), draft).Width(150).Placeholder("None").Label("Key for " + label).
						OnSubmit(func() {
							if err := a.setKey(label, *draft); err != nil {
								a.keyError = label + ": " + err.Error()
								return
							}
							a.keyError = ""
							*draft = a.defaultKey(label)
							if k, ok := a.keys[label]; ok {
								*draft = k
							}
						})
					_, custom := a.keys[label]
					reset := ui.Button(c.Key("reset"), "Reset").Label("Reset the key for " + label)
					if !custom {
						reset.Disabled(true)
					}
					if reset.Clicked() {
						a.resetKey(label)
						*draft = a.defaultKey(label)
						a.keyError = ""
					}
				})
			}
		}
	})
}
