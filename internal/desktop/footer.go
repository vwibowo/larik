package desktop

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"larik/internal/release"
	"larik/internal/session"
	"larik/internal/status"
)

// versionEvery is how often the window looks for a newer release: it
// stays open for days.
const versionEvery = 6 * time.Hour

// versionLoop looks for a newer release now and then. A failed look is
// silent, as the TUI's: the window works offline.
func (a *App) versionLoop() {
	client := &http.Client{Timeout: 5 * time.Second}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if v, err := release.Newer(ctx, a.opts.Version, release.LatestURL, client); err == nil {
			a.update(func() { a.newer = v })
		}
		cancel()
		select {
		case <-a.done:
			return
		case <-time.After(versionEvery):
		}
	}
}

// footer is the sidebar's last line: what's chosen in it and what to do
// with them, while sessions are; otherwise larik's version, a newer
// release when there is one, and what waits on you.
func (a *App) footer(c *ui.Context, t *ui.Theme) {
	a.prunePicked()
	ui.Row(c.Key("footer")).Height(30).Gap(4).Padding(6, 4, 0, 6).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		if len(a.picked) > 1 {
			a.pickedBar(c, t)
			return
		}
		a.versionLabel(c, t)
		ui.Spacer(c)
		a.attention(c, t)
	})
}

// versionLabel is larik's version, quiet, and beside it the newer
// release, in the accent colour, with a menu to get it.
func (a *App) versionLabel(c *ui.Context, t *ui.Theme) {
	current := "larik " + displayVersion(a.opts.Version)
	if a.newer == "" {
		ui.Text(c.Key("version"), current).FontSize(11).TextColor(t.TextMuted).SingleLine().Tooltip("The larik these tabs run")
		return
	}
	update := ui.Row(c.Key("update")).Gap(5).Padding(2, 6).Radius(999).AlignItems(ui.Center).Cursor(ui.CursorPointer).
		Label(fmt.Sprintf("%s is out of date: %s is available", current, a.newer)).
		Tooltip(fmt.Sprintf("%s is available. You have %s.", a.newer, displayVersion(a.opts.Version))).Children(func() {
		ui.Box(c).Size(6, 6).Radius(3).Background(t.Warning)
		ui.Text(c, current).FontSize(11).TextColor(t.TextMuted).SingleLine()
		ui.Text(c, a.newer+" available").FontSize(11).TextColor(t.Accent).SingleLine().Shrink(1)
	})
	if update.Hovered() {
		update.Background(t.SurfaceHover)
	}
	update.Menu(func(m *ui.Menu) {
		if m.Item("What's New in "+a.newer).Chosen() && a.openExternal != nil {
			a.openExternal(release.NotesURL)
		}
		if m.Item("Copy Update Command").Chosen() && a.copyText != nil {
			a.copyText(release.UpdateCommand)
		}
	})
}

// displayVersion is a version as people read it: "v0.28.0", or a
// development build's own name.
func displayVersion(v string) string {
	if v == "" {
		return "dev"
	}
	if _, ok := release.NewerVersion("0.0.0", v); ok {
		return "v" + strings.TrimPrefix(v, "v")
	}
	return v
}

// attention shows how many sessions need you, failed, or finished since
// you looked, when any do: a click opens the newest of each. Nothing
// shows while nothing waits.
func (a *App) attention(c *ui.Context, t *ui.Theme) {
	var blocked, failed, done []string
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			switch st := s.Status.State; {
			case st == status.Blocked:
				blocked = append(blocked, s.Path)
			case st == status.Error && a.unread(s):
				failed = append(failed, s.Path)
			case st == status.Done && a.unread(s):
				done = append(done, s.Path)
			}
		}
	}
	for _, g := range []struct {
		key   string
		paths []string
		icon  *ui.SVG
		color ui.Color
		label string
	}{
		{"blocked", blocked, iconPause, t.Warning, "need you"},
		{"failed", failed, iconError, t.Danger, "failed"},
		{"done", done, iconCheck, t.Success, "finished, unread"},
	} {
		if len(g.paths) == 0 {
			continue
		}
		what := fmt.Sprintf("%d %s", len(g.paths), g.label)
		chip := ui.Row(c.Key(g.key)).Gap(3).Padding(2, 6).Radius(999).Cursor(ui.CursorPointer).
			Label(what + ": open the newest").Tooltip(what).Children(func() {
			ui.Icon(c, g.icon).FontSize(12).TextColor(g.color)
			ui.Text(c, fmt.Sprint(len(g.paths))).FontSize(11)
		})
		if chip.Hovered() {
			chip.Background(t.SurfaceHover)
		}
		if chip.Clicked() {
			a.reveal(g.paths[0])
		}
	}
}

// pickedBar says how many sessions are chosen and offers what to do with
// them all.
func (a *App) pickedBar(c *ui.Context, t *ui.Theme) {
	n := len(a.picked)
	ui.Text(c.Key("picked"), fmt.Sprintf("%d selected", n)).FontSize(11).Bold().SingleLine()
	ui.Spacer(c)
	a.iconButton(c, t, "pick-archive", iconArchive, fmt.Sprintf("Archive %d sessions", n), false, func() { a.archivePicked() })
	a.iconButton(c, t, "pick-trash", iconTrash, fmt.Sprintf("Move %d sessions to the Trash…", n), false, func() { a.trashPicked() })
	a.iconButton(c, t, "pick-clear", iconClose, "Deselect all (Escape)", false, func() { a.clearPicked() })
}

// multiMods are the modifiers that add to a choice rather than replace
// it: Command on macOS, Control elsewhere, and Shift for a range.
func multiMods(mods ui.Modifiers) (toggle, extend bool) {
	add := ui.Ctrl
	if runtime.GOOS == "darwin" {
		add = ui.Super
	}
	return mods&add != 0, mods&ui.Shift != 0
}

// pickClick chooses sessions as Finder does: Command-click adds a session
// or takes it out, Shift-click takes those from the last one clicked, and
// a plain click chooses one alone, as the sidebar opens it. It reports
// whether the click chose together, and so opens nothing.
func (a *App) pickClick(path string, mods ui.Modifiers) bool {
	toggle, extend := multiMods(mods)
	switch {
	case extend:
		from := a.anchor
		if from == "" {
			from = a.focusedSession()
		}
		a.pickRange(from, path)
		a.cursor = path
	case toggle:
		if len(a.picked) == 0 {
			if f := a.focusedSession(); f != "" && f != path {
				a.picked = []string{f}
			}
		}
		if i := slices.Index(a.picked, path); i >= 0 {
			a.picked = slices.Delete(a.picked, i, i+1)
		} else {
			a.picked = append(a.picked, path)
		}
		a.anchor, a.cursor = path, path
	default:
		a.picked, a.anchor, a.cursor = nil, path, ""
		return false
	}
	return true
}

// pickRange chooses the sessions from one to another as the sidebar
// lists them, both included.
func (a *App) pickRange(from, to string) {
	i, j := slices.Index(a.rows, from), slices.Index(a.rows, to)
	if i < 0 {
		i = j
	}
	if j < 0 {
		return
	}
	if i > j {
		i, j = j, i
	}
	a.picked = slices.Clone(a.rows[i : j+1])
	if a.anchor == "" {
		a.anchor = from
	}
}

// focusedSession is the focused tab's session, or "".
func (a *App) focusedSession() string {
	if f := a.focused(); f != nil {
		return f.path
	}
	return ""
}

// prunePicked lets go of chosen sessions that have gone: archived,
// trashed, or moved.
func (a *App) prunePicked() {
	a.picked = slices.DeleteFunc(a.picked, func(path string) bool {
		_, _, ok := a.find(path)
		return !ok
	})
	if len(a.picked) < 2 && len(a.picked) > 0 && !slices.Contains(a.rows, a.picked[0]) {
		a.picked = nil
	}
}

func (a *App) clearPicked() { a.picked, a.anchor, a.cursor = nil, "", "" }

// pickedMenu is a chosen session's menu while several are chosen: what
// to do with them all.
func (a *App) pickedMenu(m *ui.Menu) {
	n := len(a.picked)
	if m.Item(fmt.Sprintf("Open %d Sessions", n)).Chosen() {
		for _, path := range slices.Clone(a.picked) {
			a.selectSession(path)
		}
	}
	allPinned := !slices.ContainsFunc(a.picked, func(p string) bool { return !slices.Contains(a.pinned, p) })
	if allPinned {
		if m.Item(fmt.Sprintf("Unpin %d Sessions", n)).Chosen() {
			for _, path := range a.picked {
				a.pin(path, false)
			}
		}
	} else if m.Item(fmt.Sprintf("Pin %d Sessions to Top", n)).Chosen() {
		for _, path := range a.picked {
			if !slices.Contains(a.pinned, path) {
				a.pin(path, true)
			}
		}
	}
	if m.Item(fmt.Sprintf("Archive %d Sessions", n)).Chosen() {
		a.archivePicked()
	}
	if m.Item(fmt.Sprintf("Move %d Sessions to Trash…", n)).Chosen() {
		a.trashPicked()
	}
	m.Separator()
	if m.Item("Deselect All").Chosen() {
		a.clearPicked()
	}
}

// archivePicked archives the chosen sessions, keeping chosen those that
// can't go, such as one working.
func (a *App) archivePicked() {
	var left []string
	for _, path := range a.picked {
		if !a.archive(path) {
			left = append(left, path)
		}
	}
	a.picked = left
}

// trashPicked moves the chosen sessions to the Trash, after asking once.
// Those that can't go, such as one working, stay, and the notice says
// why.
func (a *App) trashPicked() {
	var movable []string
	for _, path := range a.picked {
		if msg := a.unmovable(path); msg != "" {
			a.notice = msg
			continue
		}
		movable = append(movable, path)
	}
	if len(movable) == 0 {
		return
	}
	title := fmt.Sprintf("Move %d sessions to the Trash?", len(movable))
	if len(movable) == 1 {
		s, _, _ := a.findAny(movable[0])
		title = fmt.Sprintf("Move “%s” to the Trash?", sessionLabel(s))
	}
	a.confirmAsk(title,
		"Their conversations, and what larik kept beside them, go to the Trash in folders of their own, where you can get them back until it's emptied.",
		"Move to Trash", func() {
			for _, path := range movable {
				a.trashNow(path)
			}
			a.picked = slices.DeleteFunc(a.picked, func(p string) bool { return slices.Contains(movable, p) })
		})
}

// sessionLabel is a session's name as the sidebar shows it.
func sessionLabel(s session.Indexed) string {
	if s.Title == "" {
		return "Untitled session"
	}
	return s.Title
}

// deleteMods are the modifiers of the key that moves to the Trash:
// Command+Delete on macOS, as Finder's, and Delete alone elsewhere.
func deleteMods() ui.Modifiers {
	if runtime.GOOS == "darwin" {
		return ui.Super
	}
	return 0
}

// announcePicked tells a screen reader how many sessions are chosen,
// which no focus change says.
func (a *App) announcePicked(c *ui.Context) {
	switch n := len(a.picked); {
	case n > 1:
		c.Announce(fmt.Sprintf("%d sessions selected", n))
	case n == 1:
		c.Announce("1 session selected")
	}
}

// pickStep moves the keyboard by one session and chooses those from the
// anchor to it, as Shift+Down and Shift+Up do in Finder; from is where
// the sidebar's keyboard is.
func (a *App) pickStep(by int, from string) {
	at := slices.Index(a.rows, cmp.Or(a.cursor, from))
	if at < 0 {
		return
	}
	if a.anchor == "" || !slices.Contains(a.rows, a.anchor) {
		a.anchor = a.rows[at]
	}
	next := a.rows[min(max(at+by, 0), len(a.rows)-1)]
	a.pickRange(a.anchor, next)
	a.cursor = next
}
