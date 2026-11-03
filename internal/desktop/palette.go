package desktop

import (
	"cmp"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// The command palette (Shift+Command+P), as cmux and VS Code have one:
// everything the window does, by name, in one list you type to narrow.
// What the focused session can do comes first, as answering its prompt
// or merging its worktree, which otherwise sit in its right-click menu;
// then what the changes pane can do while it's open; then the menu bar's
// commands, with their keys; then the sessions, to open one.

// paletteItem is a line of the palette: what it says, where it's from
// or what it's about, its key, what it does, and whether it opens a
// session.
type paletteItem struct {
	label, detail, key string
	run                func()
	session            bool
}

// paletteSessions is how many sessions the palette lists before
// anything is typed: the newest.
const paletteSessions = 5

// paletteLabel is the palette's own item in the View menu.
const paletteLabel = "Command Palette…"

// openPalette shows the palette, empty, or closes it.
func (a *App) openPalette() {
	if a.paletteOpen {
		a.closePalette()
		return
	}
	a.paletteOpen, a.paletteQuery, a.palettePick, a.focusPalette = true, "", 0, true
}

// closePalette hides it, giving the keyboard back to the focused tab.
func (a *App) closePalette() {
	a.paletteOpen = false
	if f := a.focused(); f != nil {
		a.focusNext = f
	}
}

// paletteItems is everything the palette offers now, in order.
func (a *App) paletteItems() []paletteItem {
	var items []paletteItem
	add := func(label, detail, key string, run func()) {
		items = append(items, paletteItem{label: label, detail: detail, key: key, run: run})
	}
	// The focused session's own: what its menus offer.
	if f := a.focused(); f != nil {
		title := a.tabTitle(f)
		if s, p, ok := a.find(f.path); ok {
			if _, ok := a.answerable(s); ok {
				plan := s.Status.ReviewsPlan()
				for _, an := range []answer{allowOnce, allowAlways, deny} {
					_, tip := an.button(plan)
					add(an.label(plan), tip, "", func() { a.answer(s.Path, an) })
				}
			}
			if _, err := os.Stat(s.Cwd); err == nil && a.inWorktree(s.Cwd) {
				into := cmp.Or(a.git[p.Root].Branch, "the Checkout")
				add("Merge Worktree into "+into+"…", title, "", func() { a.askWorktree(s, p.Root, true) })
				add("Remove Worktree…", title, "", func() { a.askWorktree(s, p.Root, false) })
			}
			add("Rename Session…", title, "", func() { a.askRename(s.Path) })
			add("Fork into a New Tab", title, "", func() { a.fork(s.Path) })
			if a.copyText != nil {
				add("Copy Session ID", s.ID, "", func() { a.copyText(s.ID) })
			}
		}
		if f.exited && f.code != 0 {
			add("Restart", title, "", func() { a.restart(f) })
		}
	}
	// The changes pane's, while it's open.
	if ch := a.changes; ch != nil && ch.loaded && ch.err == "" && ch.busy == "" {
		where := "Changes in " + cmp.Or(ch.branch, "the checkout")
		if ch.against == compareCommit && len(ch.files) > 0 {
			if ch.stagedCount() < len(ch.files) {
				add("Stage All", where, "", func() { a.stageAll(ch, true) })
			}
			if ch.stagedCount() > 0 {
				add("Unstage All", where, "", func() { a.stageAll(ch, false) })
			}
		}
		if ch.base != "" {
			if ch.against == compareCommit {
				add("Compare Since "+ch.base, where, "", func() { ch.compare(compareBase) })
			} else {
				add("Compare Uncommitted", where, "", func() { ch.compare(compareCommit) })
			}
		}
		if ch.canPullRequest() {
			add("Pull Request…", ch.branch+" into "+strings.TrimPrefix(ch.base, "origin/"), "", func() { a.askPullRequest(ch) })
		}
	}
	// The menu bar's, but for AppKit's roles, which only its menus run,
	// the palette itself and the search, and the open tabs, which the
	// sessions are; the app's menu and Help, used least, last.
	tabs := len(a.tabCommands())
	groups := a.menuBar()
	slices.SortStableFunc(groups, func(x, y menuGroup) int {
		last := func(g menuGroup) bool { return g.title == "" || g.title == "Help" }
		return cmp.Compare(boolInt(last(x)), boolInt(last(y)))
	})
	for _, g := range groups {
		menu, cmds := cmp.Or(g.title, "Larik"), g.items
		if g.title == "Window" {
			cmds = cmds[:len(cmds)-tabs]
		}
		for _, cmd := range cmds {
			if cmd.run == nil || cmd.label == "" || cmd.label == paletteLabel || cmd.label == "Search Sessions…" {
				continue
			}
			if !a.split() && strings.Contains(cmd.label, "Pane") {
				continue // nothing to zoom, close or move to
			}
			add(cmd.label, menu, keyText(cmd.key), cmd.run)
		}
	}
	// And every session, to open: the sidebar's, newest project first.
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			if a.hidden[s.Path] {
				continue
			}
			detail := p.Name
			if s.Subdir != "" {
				detail += " · " + s.Subdir
			}
			path := s.Path
			items = append(items, paletteItem{label: cmp.Or(s.Title, "Untitled session"), detail: detail, run: func() { a.reveal(path) }, session: true})
		}
	}
	return items
}

// paletteMatches is what the palette shows for its query: every item
// with every word of it, those whose label starts with it first; with
// nothing typed, everything but the older sessions.
func (a *App) paletteMatches() []paletteItem {
	items := a.paletteItems()
	query := strings.ToLower(strings.TrimSpace(a.paletteQuery))
	if query == "" {
		sessions := 0
		return slices.DeleteFunc(items, func(it paletteItem) bool {
			if it.session {
				sessions++
			}
			return sessions > paletteSessions
		})
	}
	words := strings.Fields(query)
	var first, rest []paletteItem
	for _, it := range items {
		hay := strings.ToLower(it.label + " " + it.detail)
		if slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(hay, w) }) {
			continue
		}
		if strings.HasPrefix(strings.ToLower(it.label), query) {
			first = append(first, it)
		} else {
			rest = append(rest, it)
		}
	}
	return append(first, rest...)
}

// runPick runs the chosen line, closing the palette first so that what
// it opens, as a sheet, has the window; then the keyboard goes back to
// the focused tab, unless what ran gave it to a field of its own.
func (a *App) runPick() {
	found := a.paletteMatches()
	if len(found) == 0 {
		return
	}
	run := found[min(a.palettePick, len(found)-1)].run
	a.paletteOpen = false
	run()
	f := a.focused()
	if f != nil && a.focusNext == nil && !a.focusSearch && !a.focusRename && !f.focusFind {
		a.focusNext = f
	}
}

// movePalettePick moves the chosen line by, wrapping around, and
// scrolls to it.
func (a *App) movePalettePick(by int) {
	if n := len(a.paletteMatches()); n > 0 {
		a.palettePick = ((a.palettePick+by)%n + n) % n
		a.paletteScroll = true
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// commandPalette draws the palette while it's open: a field to type in, over
// the lines it matches, the chosen one marked.
func (a *App) commandPalette(c *ui.Context, t *ui.Theme) {
	if !a.paletteOpen {
		return
	}
	m := ui.Modal(c, &a.paletteOpen, func() {
		ui.Column(c).Width(560).Gap(6).Children(func() {
			field := ui.SearchField(c.Key("palette-query"), &a.paletteQuery).Placeholder("Type a command, or a session to open").Label("Command palette").
				OnChange(func() { a.palettePick = 0 }).
				OnSubmit(a.runPick).
				OnShortcut(0, ui.KeyDown, func() { a.movePalettePick(1) }).
				OnShortcut(0, ui.KeyUp, func() { a.movePalettePick(-1) }).
				OnShortcut(0, ui.KeyEscape, a.closePalette).
				OnShortcut(windowMod()|ui.Shift, ui.KeyP, a.closePalette) // the field has the window's keys
			if a.focusPalette {
				field.Focus()
				a.focusPalette = false
			}
			found := a.paletteMatches()
			if len(found) == 0 {
				ui.Text(c, "Nothing matches.").FontSize(12).TextColor(t.TextMuted).Padding(10, 8)
				return
			}
			pick := min(a.palettePick, len(found)-1)
			ui.Scroll(c).MaxHeight(380).Children(func() {
				ui.Column(c).Gap(1).Children(func() {
					for i, it := range found {
						a.paletteRow(c, t, i, it, i == pick)
					}
				})
			})
		})
	})
	if m.Dismissed() {
		a.closePalette()
	}
}

// paletteRow is one line of the palette.
func (a *App) paletteRow(c *ui.Context, t *ui.Theme, i int, it paletteItem, picked bool) {
	row := ui.Row(c.Key(fmt.Sprint("palette-", i, it.label))).Gap(10).Padding(6, 10).Radius(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(it.label).Children(func() {
		label := ui.Text(c, it.label).FontSize(13).SingleLine().Shrink(1)
		detail := ui.Text(c, it.detail).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1).Grow(1)
		if picked {
			label.TextColor(ui.Hex("#ffffff"))
			detail.TextColor(ui.Hex("#ffffff").Alpha(0.75))
		}
		if it.key != "" {
			k := ui.Text(c, it.key).FontSize(12).TextColor(t.TextMuted).SingleLine()
			if picked {
				k.TextColor(ui.Hex("#ffffff").Alpha(0.85))
			}
		}
	})
	if picked && a.paletteScroll {
		row.ScrollIntoView()
		a.paletteScroll = false
	}
	switch {
	case picked:
		row.Background(t.Accent)
	case row.Hovered():
		row.Background(t.SurfaceHover)
	}
	if row.Clicked() {
		a.palettePick = i
		a.runPick()
	}
}

// windowMod is what the window's keys take: Command on macOS, else
// Control+Shift (see shortcuts).
func windowMod() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super
	}
	return ui.Ctrl | ui.Shift
}

// keyText is a menu key, as "Shift+Cmd+G", as this system shows it: ⇧⌘G
// on macOS, and elsewhere the Control+Shift the window's keys take
// there (see shortcuts).
func keyText(key string) string {
	if key == "" {
		return ""
	}
	parts := strings.Split(key, "+")
	k := parts[len(parts)-1]
	mods := parts[:len(parts)-1]
	if runtime.GOOS == "darwin" {
		var b strings.Builder
		for _, m := range []struct{ name, sym string }{{"Ctrl", "⌃"}, {"Alt", "⌥"}, {"Shift", "⇧"}, {"Cmd", "⌘"}} {
			if slices.Contains(mods, m.name) {
				b.WriteString(m.sym)
			}
		}
		if sym, ok := map[string]string{"Left": "←", "Right": "→", "Up": "↑", "Down": "↓", "Enter": "↩"}[k]; ok {
			k = sym
		}
		return b.String() + k
	}
	out := "Ctrl+"
	if slices.Contains(mods, "Alt") {
		out += "Alt+"
	}
	return out + "Shift+" + k
}
