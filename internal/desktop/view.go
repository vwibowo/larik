package desktop

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"larik/internal/session"
	"larik/internal/status"
)

// mono is the terminal's font family.
const mono = "SF Mono, Menlo, Consolas, DejaVu Sans Mono, monospace"

// Icons, in Lucide's style, drawn thin as SF Symbols are: stroked, round,
// 24×24.
func svg(body string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round">` + body + `</svg>`))
}

var (
	iconChat  = svg(`<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>`)
	iconTerm  = svg(`<path d="m4 17 6-6-6-6M12 19h8"/>`)
	iconPlus  = svg(`<path d="M12 5v14M5 12h14"/>`)
	iconClose = svg(`<path d="M18 6 6 18M6 6l12 12"/>`)
	iconPause = svg(`<circle cx="12" cy="12" r="10"/><path d="M10 15V9M14 15V9"/>`)
	iconCheck = svg(`<path d="M20 6 9 17l-5-5"/>`)
	iconError = svg(`<circle cx="12" cy="12" r="10"/><path d="m15 9-6 6M9 9l6 6"/>`)
	iconDiff  = svg(`<path d="M12 3v14M5 10h14M5 21h14"/>`)
	iconSplit = svg(`<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M12 3v18"/>`)
	iconPanel = svg(`<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/>`)
	iconUp    = svg(`<path d="m18 15-6-6-6 6"/>`)
	iconDown  = svg(`<path d="m6 9 6 6 6-6"/>`)
	iconRight = svg(`<path d="m9 18 6-6-6-6"/>`)
	iconBell  = svg(`<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/>`)
)

// macOS's colours: the content's background and labels, the sidebar
// where the window shows no material, the tab bar behind the tabs not
// shown, and selected text.
var (
	contentLight, contentDark   = ui.Hex("#ffffff"), ui.Hex("#1e1e1e")
	labelLight, labelDark       = ui.Hex("#262626"), ui.Hex("#dfdfdf")
	sidebarLight, sidebarDark   = ui.Hex("#ebebeb"), ui.Hex("#2a2a2a")
	tabsLight, tabsDark         = ui.Hex("#e6e6e6"), ui.Hex("#151515")
	selectedLight, selectedDark = ui.Hex("#b3d7ff"), ui.Hex("#3f638b")
)

// palette is the window's theme: macOS's colours, on the system's theme,
// which has the accent colour chosen in System Settings.
func (a *App) palette(base *ui.Theme) *ui.Theme {
	i := 0
	if base.Dark {
		i = 1
	}
	if t := a.theme[i]; t != nil && t.Accent == base.Accent && t.FontSize == base.FontSize {
		return t
	}
	t := *base
	if t.Dark {
		t.Background = contentDark
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#323232"), ui.Hex("#3b3b3b"), ui.Hex("#464646")
		t.Border = ui.RGBA(255, 255, 255, 0.1)
		t.Text, t.TextMuted = labelDark, ui.Hex("#8d8d8d")
		t.Success, t.Warning, t.Danger = ui.Hex("#32d74b"), ui.Hex("#ff9f0a"), ui.Hex("#ff453a")
	} else {
		t.Background = contentLight
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#f2f2f2"), ui.Hex("#e8e8e8"), ui.Hex("#dddddd")
		t.Border = ui.RGBA(0, 0, 0, 0.1)
		t.Text, t.TextMuted = labelLight, ui.Hex("#808080")
		t.Success, t.Warning, t.Danger = ui.Hex("#28cd41"), ui.Hex("#ff9500"), ui.Hex("#ff3b30")
	}
	t.Radius = 6
	a.theme[i] = &t
	return &t
}

// sidebarColor is the sidebar's background where the window shows no
// material: off macOS, or with Reduce Transparency.
func sidebarColor(t *ui.Theme) ui.Color {
	if t.Dark {
		return sidebarDark
	}
	return sidebarLight
}

// terminalTheme is the terminals' colors: the content's background and
// text, macOS's selected text, and Visual Studio Code's palette.
func (a *App) terminalTheme(dark bool) *terminal.Theme {
	i := 0
	if dark {
		i = 1
	}
	if a.termTh[i] != nil {
		return a.termTh[i]
	}
	th := terminal.LightTheme()
	th.Background, th.Foreground, th.Selection = contentLight, labelLight, selectedLight
	if dark {
		th = terminal.DarkTheme()
		th.Background, th.Foreground, th.Selection = contentDark, labelDark, selectedDark
	}
	th.Cursor = th.Foreground.Alpha(0.7)
	a.termTh[i] = th
	return th
}

// stateColor is the colour of a status, for tab stripes and markers.
func stateColor(t *ui.Theme, r status.Report) ui.Color {
	switch r.State {
	case status.Working:
		return t.Accent
	case status.Blocked:
		return t.Warning
	case status.Done:
		return t.Success
	case status.Error:
		return t.Danger
	}
	return t.TextMuted.Alpha(0.5)
}

func (a *App) view(c *ui.Context) {
	t := a.palette(c.Theme())
	a.shortcuts(c)
	// As a macOS window draws it: the sidebar full height, in the
	// window's material, and beside it the content, opaque, under a
	// toolbar and the tabs.
	vib := c.Vibrancy()
	if vib {
		c.Root().Background(ui.Transparent)
	}
	shell := ui.Themed(c, t).Fill()
	if vib {
		shell.Background(ui.Transparent) // the material shows through the sidebar
	}
	shell.Children(func() {
		content := func() {
			ui.Column(c).Fill().Background(t.Background).Children(func() {
				a.toolbar(c, t)
				a.tabBar(c, t)
				a.main(c, t)
			})
		}
		if a.noSidebar {
			content()
		} else {
			ui.Split(c, &a.sidebarW, func() { a.sidebar(c, t, vib) }, content).Grow(1)
		}
		a.confirmClose(c)
		a.confirmLeave(c)
		a.confirmAsked(c)
	})
	a.saveState()
	if title := a.windowTitle(); a.setTitle != nil && title != a.titled {
		a.titled = title
		a.setTitle(title)
	}
	if said := a.menuTabs(); a.setMenu != nil && said != a.menuSaid {
		a.menuSaid = said
		a.setMenu()
	}
}

// confirmClose asks whether to close tabs whose larik is working.
func (a *App) confirmClose(c *ui.Context) {
	var title, msg string
	if a.confirming {
		working := a.closingWorking()
		if len(a.closing) == 1 {
			title = fmt.Sprintf("Close “%s”?", a.tabTitle(a.closing[0]))
			msg = "larik is working in this tab. Closing it stops the turn; the session stays, so you can resume it."
		} else {
			title = fmt.Sprintf("Close %d tabs?", len(a.closing))
			msg = fmt.Sprintf("%d of them are working. Closing stops their turns; the sessions stay, so you can resume them.", len(working))
			if len(working) == 1 {
				msg = fmt.Sprintf("“%s” is working. Closing stops its turn; the session stays, so you can resume it.", a.tabTitle(working[0]))
			}
		}
	}
	if a.ask != nil {
		if a.confirming && !a.asking {
			a.asking = true
			a.ask(title, msg, "Close", func(yes bool) {
				a.asking = false
				if yes {
					a.closeNow()
				} else {
					a.cancelClose()
				}
			})
		}
		return
	}
	switch ui.AlertDialog(c.Key("confirm-close"), &a.confirming, title, msg, "Cancel", "Close") {
	case 1:
		a.closeNow()
	case 0, ui.AlertDismissed:
		a.cancelClose()
	}
}

// confirmLeave asks whether to close the window, or quit, while tabs are
// working.
func (a *App) confirmLeave(c *ui.Context) {
	var title, msg, yes string
	if a.confirmingLeave {
		title, yes = "Close the window?", "Close Window"
		if a.quitting {
			title, yes = "Quit Larik?", "Quit"
		}
		working := a.workingTabs()
		msg = fmt.Sprintf("%d sessions are working. Leaving stops their turns; the sessions stay, and their tabs open again next time.", len(working))
		if len(working) == 1 {
			msg = fmt.Sprintf("“%s” is working. Leaving stops its turn; the session stays, and its tab opens again next time.", a.tabTitle(working[0]))
		}
	}
	leave := func(yes bool) {
		if !yes {
			a.quitting = false
			return
		}
		a.leaving = true
		if a.leave != nil {
			a.leave(a.quitting)
		}
	}
	if a.ask != nil {
		if a.confirmingLeave && !a.asking {
			a.asking = true
			a.ask(title, msg, yes, func(ok bool) {
				a.asking, a.confirmingLeave = false, false
				leave(ok)
			})
		}
		return
	}
	switch ui.AlertDialog(c.Key("confirm-leave"), &a.confirmingLeave, title, msg, "Cancel", yes) {
	case 1:
		leave(true)
	case 0, ui.AlertDismissed:
		leave(false)
	}
}

// confirmation is a question confirmAsk put, and what a yes does.
type confirmation struct {
	title, msg, yes string
	then            func()
}

// confirmAsk asks whether to do something, as a sheet: then runs on a
// yes.
func (a *App) confirmAsk(title, msg, yes string, then func()) {
	a.asked, a.askedOpen = &confirmation{title: title, msg: msg, yes: yes, then: then}, true
}

// confirmAsked shows what confirmAsk asks, until it's answered.
func (a *App) confirmAsked(c *ui.Context) {
	q := a.asked
	if q == nil {
		return
	}
	answer := func(yes bool) {
		a.asked, a.askedOpen = nil, false
		if yes {
			q.then()
		}
	}
	if a.ask != nil {
		if !a.asking {
			a.asking = true
			a.ask(q.title, q.msg, q.yes, func(yes bool) {
				a.asking = false
				answer(yes)
			})
		}
		return
	}
	switch ui.AlertDialog(c.Key("confirm-asked"), &a.askedOpen, q.title, q.msg, "Cancel", q.yes) {
	case 1:
		answer(true)
	case 0, ui.AlertDismissed:
		answer(false)
	}
}

// shortcuts are the window's keys. Command keys on macOS never reach the
// terminal; elsewhere they take Control+Shift, as terminal apps do, so
// that Control keys stay larik's. Shift adds nothing there, so no two
// shortcuts differ by Shift alone.
func (a *App) shortcuts(c *ui.Context) {
	mod := ui.Ctrl | ui.Shift
	if runtime.GOOS == "darwin" {
		mod = ui.Super
	}
	switch {
	case c.Shortcut(mod|ui.Alt, ui.KeyT):
		a.newWorktreeSession(a.currentDir())
	case c.Shortcut(mod, ui.KeyT):
		a.newTab(a.currentDir(), "", "")
	case c.Shortcut(mod, ui.KeyW):
		if t := a.focused(); t != nil {
			a.askClose([]*tab{t}, nil)
		}
	case c.Shortcut(mod, ui.KeyD):
		a.splitRight()
	case c.Shortcut(mod, ui.KeyO):
		a.openFolder()
	case c.Shortcut(mod, ui.KeyComma):
		a.openSettings()
	case c.Shortcut(mod|ui.Shift, ui.KeyG):
		a.toggleChanges()
	case c.Shortcut(mod, ui.KeyJ):
		a.nextWaiting()
	case c.Shortcut(mod|ui.Shift, ui.KeyU):
		a.latestUnread()
	case c.Shortcut(mod|ui.Shift, ui.KeyN):
		a.openShell()
	case c.Shortcut(mod, ui.KeyK):
		a.focusSearch, a.noSidebar = true, false
	case c.Shortcut(mod, ui.KeyF):
		a.openFind()
	case c.Shortcut(mod, ui.KeyB):
		a.noSidebar = !a.noSidebar
	case c.Shortcut(mod|ui.Shift, ui.KeyBracketRight):
		a.cycle(1)
	case c.Shortcut(mod|ui.Shift, ui.KeyBracketLeft):
		a.cycle(-1)
	}
	// Off macOS the shortcuts take Shift already: there, the tab menu
	// reopens a tab.
	if runtime.GOOS == "darwin" && c.Shortcut(ui.Super|ui.Shift, ui.KeyT) {
		a.reopenClosed()
	}
	for i, k := range []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9} {
		if c.Shortcut(mod, k) && len(a.tabs) > 0 {
			a.activate(a.tabs[min(i, len(a.tabs)-1)])
		}
	}
}

// cycle shows the tab by after the open one, wrapping around.
func (a *App) cycle(by int) {
	n := len(a.tabs)
	if n == 0 {
		return
	}
	i := 0
	for j, t := range a.tabs {
		if t == a.focused() {
			i = j
		}
	}
	a.activate(a.tabs[((i+by)%n+n)%n])
}

// toolbarHeight is the height of macOS's unified toolbar, over the
// sidebar and the content.
const toolbarHeight = 52

// toolbar is the content's title bar, as macOS draws a unified toolbar:
// the focused session's title and where it is, and buttons to split,
// show the changes, and start a session. It moves the window.
func (a *App) toolbar(c *ui.Context, t *ui.Theme) {
	bar := c.TitleBar()
	start := float32(16)
	if a.noSidebar {
		start = bar.Left + 10
	}
	ui.Row(c).Height(max(bar.Height, toolbarHeight)).PaddingStart(start).PaddingEnd(bar.Right+10).Gap(4).AlignItems(ui.Center).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
		if a.noSidebar {
			a.sidebarButton(c, t)
			ui.Box(c).Width(6)
		}
		title, where := "Larik", a.host
		f := a.focused()
		if f != nil {
			title, where = a.tabTitle(f), a.where(f)
		}
		head := ui.Column(c).Gap(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if f != nil {
					statusDot(c, t, a.tabStatus(f))
				}
				ui.Text(c, title).Bold().FontSize(13).SingleLine().Shrink(1)
			})
			sub := ui.Text(c, where).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
			if f != nil {
				sub.Tooltip(tildePath(f.dir))
			}
		})
		// The title is the focused tab's: its menu is the tab's, which with
		// one tab has no tab bar to show it.
		if f != nil {
			head.ContextMenu(func(m *ui.Menu) { a.tabMenu(m, f) })
			if len(a.tabs) == 1 {
				head.Label(title + " tab")
			}
		}
		ui.Spacer(c)
		if a.active != nil {
			// A toggle, as the changes button is: pressed in while split.
			tip, fn := "Split: a new session beside this one ("+keyLabel(false, "D")+"), or drop a session here", a.splitRight
			if a.right != nil {
				tip, fn = "Close the split (the tab stays open)", a.unsplit
			}
			split := a.iconButton(c, t, "split", iconSplit, tip, a.right != nil, fn)
			if d, ok := ui.Drop[sessionDrag](split); ok {
				a.dropSession(string(d), true)
			}
			if _, ok := ui.DragOver[sessionDrag](split); ok {
				split.Background(t.Accent.Alpha(0.25))
			}
			a.iconButton(c, t, "changes", iconDiff, "Changes in this project ("+keyLabel(true, "G")+")", a.changes != nil, a.toggleChanges)
		}
		a.bell(c, t)
		a.iconButton(c, t, "new", iconPlus, "New session", false, func() { a.newTab(a.currentDir(), "", "") }).
			Tooltip("New session (" + keyLabel(false, "T") + "), or right-click for a terminal").
			ContextMenu(func(m *ui.Menu) {
				if m.Item("New Session").Chosen() {
					a.newTab(a.currentDir(), "", "")
				}
				if m.Item("New Session in Worktree").Chosen() {
					a.newWorktreeSession(a.currentDir())
				}
				if m.Item("New Terminal").Chosen() {
					a.openShell()
				}
			})
	})
}

// where says where a tab's session is and what it's doing: its project
// and folder, its status, and its model.
func (a *App) where(tb *tab) string {
	st := a.tabStatus(tb)
	s, p, ok := a.find(tb.path)
	where := filepath.Base(tb.dir)
	if tb.shell() && !ok {
		what := cmp.Or(tb.run, "shell")
		if tb.exited {
			what = st.Msg
		}
		return where + " — " + what
	}
	if ok {
		where = p.Name
		if s.Subdir != "" {
			where += "/" + s.Subdir
		}
	}
	what := string(st.State)
	if st.Msg != "" && (st.State == status.Blocked || st.State == status.Error) {
		what = st.Msg
	}
	where += " — " + what
	if ok && s.Model != "" {
		where += " · " + s.Model
	}
	return where
}

// statusDot is a small dot in a status's colour.
func statusDot(c *ui.Context, t *ui.Theme, st status.Report) {
	ui.Box(c).Size(7, 7).Radius(4).Background(stateColor(t, st))
}

// sidebarButton shows or hides the sidebar, beside the window's buttons.
func (a *App) sidebarButton(c *ui.Context, t *ui.Theme) {
	tip := "Hide the sidebar (" + keyLabel(false, "B") + ")"
	if a.noSidebar {
		tip = "Show the sidebar (" + keyLabel(false, "B") + ")"
	}
	a.iconButton(c, t, "sidebar", iconPanel, tip, false, func() { a.noSidebar = !a.noSidebar })
}

// tabBar has a tab per open session once there are two, as Terminal and
// Safari draw them: equally wide, the focused one in the content's
// colour, the others a step darker.
func (a *App) tabBar(c *ui.Context, t *ui.Theme) {
	if len(a.tabs) < 2 {
		return
	}
	bg := tabsLight
	if t.Dark {
		bg = tabsDark
	}
	ui.Row(c).Height(28).AlignItems(ui.Stretch).Background(bg).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		// A copy: closing a tab from its close button or menu changes
		// a.tabs.
		for i, tb := range slices.Clone(a.tabs) {
			a.tab(c, t, tb, i > 0)
		}
	})
}

// tab is a session's tab, with a dot in its status colour and a close
// button that shows over it.
func (a *App) tab(c *ui.Context, t *ui.Theme, tb *tab, divider bool) {
	active := tb == a.focused()
	shown := tb == a.active || tb == a.right
	cell := ui.Row(c.Key(tb.id)).Label(a.tabTitle(tb)+" tab").Grow(1).Basis(0).MinWidth(0).Gap(6).Padding(0, 8).AlignItems(ui.Center)
	if divider {
		cell.BorderWidth(0, 0, 0, 1).BorderColor(t.Border)
	}
	hover := cell.Hovered()
	switch {
	case active:
		cell.Background(t.Background)
	case hover:
		cell.Background(t.Text.Alpha(0.04))
	}
	closing := false
	cell.Children(func() {
		x := ui.Box(c.Key("close")).Padding(2).Radius(4).Label("Close tab").Tooltip("Close tab").Children(func() {
			ui.Icon(c, iconClose).FontSize(11).TextColor(t.TextMuted)
		})
		if !hover {
			x.Opacity(0)
		}
		if x.Hovered() {
			x.Background(t.Text.Alpha(0.08))
		}
		if x.Clicked() {
			closing = true
			a.askClose([]*tab{tb}, nil)
		}
		ui.Spacer(c)
		statusDot(c, t, a.tabStatus(tb))
		label := ui.Text(c, a.tabTitle(tb)).FontSize(12).SingleLine().Shrink(1)
		if !active && !shown {
			label.TextColor(t.TextMuted)
		}
		ui.Spacer(c)
		ui.Box(c).Width(19) // as wide as the close button, to center the title
	})
	if cell.Clicked() && !closing {
		a.activate(tb)
	}
	cell.ContextMenu(func(m *ui.Menu) { a.tabMenu(m, tb) })
	// A tab dragged onto another takes its place.
	cell.Drag(tb)
	if from, ok := ui.Drop[*tab](cell); ok {
		a.moveTab(from, tb)
	}
	if from, ok := ui.DragOver[*tab](cell); ok && from != tb {
		cell.Background(t.Accent.Alpha(0.15))
	} else if a.needsLook(tb) {
		// Its session needs you, or finished, while you look elsewhere.
		color := a.ringColor(t, tb)
		cell.Border(2, color).Background(color.Alpha(0.1))
	}
}

// tabMenu is what a tab's context menu offers.
func (a *App) tabMenu(m *ui.Menu, tb *tab) {
	if s, _, ok := a.find(tb.path); ok {
		a.answerMenu(m, s)
	}
	if tb != a.active && tb != a.right && m.Item("Open in Split").Chosen() {
		a.showInSplit(tb)
	}
	if tb.exited && tb.code != 0 && m.Item("Restart").Chosen() {
		a.restart(tb)
	}
	if m.Item("Close Tab").Chosen() {
		a.askClose([]*tab{tb}, nil)
	}
	if len(a.tabs) > 1 && m.Item("Close Other Tabs").Chosen() {
		a.closeOthers(tb)
	}
	if len(a.closed) > 0 && m.Item("Reopen Closed Tab").Chosen() {
		a.reopenClosed()
	}
	if tb.path == "" {
		return
	}
	if m.Item("Rename…").Chosen() {
		a.askRename(tb.path)
	}
	if s, p, ok := a.find(tb.path); ok {
		a.worktreeMenu(m, s, p.Root)
	}
	m.Separator()
	if s, _, ok := a.find(tb.path); ok && m.Item("Copy Session ID").Chosen() && a.copyText != nil {
		a.copyText(s.ID)
	}
	if m.Item(revealLabel()).Chosen() && a.showFile != nil {
		a.showFile(tb.path)
	}
}

// sidebar lists every project's sessions, newest project first, and the
// inbox: what needs you, failed, or finished since you looked.
func (a *App) sidebar(c *ui.Context, t *ui.Theme, vib bool) {
	// A folder dropped from the file manager starts a session there.
	side := ui.Column(c).Fill().Padding(0, 8, 8, 8).Gap(6).Children(func() {
		// The window's buttons sit over the top, which moves the window.
		bar := c.TitleBar()
		ui.Row(c).Height(max(bar.Height, toolbarHeight)).PaddingStart(bar.Left).AlignItems(ui.Center).DragWindow().Children(func() {
			ui.Spacer(c)
			a.sidebarButton(c, t)
		})
		// Up and Down choose a match, the first to start with, and Enter
		// opens it; Escape clears the field, and then gives the keyboard
		// back to the terminal.
		search := ui.SearchField(c.Key("search"), &a.query).Placeholder("Search sessions").Label("Search sessions ("+keyLabel(false, "K")+")").
			OnChange(func() { a.pick = 0 }).
			OnSubmit(a.openPicked).
			OnShortcut(0, ui.KeyDown, func() { a.movePick(1) }).
			OnShortcut(0, ui.KeyUp, func() { a.movePick(-1) }).
			OnShortcut(0, ui.KeyEscape, func() { a.focusNext = a.focused() })
		if a.focusSearch {
			search.Focus()
			a.focusSearch = false
		}
		selected := ""
		if f := a.focused(); f != nil {
			selected = f.path
			if f.shell() && f.path == "" {
				selected = tabKey(f)
			}
		}
		if found := a.matches(); strings.TrimSpace(a.query) != "" && len(found) > 0 {
			selected = found[min(a.pick, len(found)-1)].Path // the match Enter opens
		}
		sb := ui.Sidebar(c, &selected, func() {
			if strings.TrimSpace(a.query) != "" {
				a.results(c, t)
				return
			}
			a.terminals(c, t)
			a.pinnedSection(c, t)
			for _, p := range a.projects {
				a.project(c, t, p)
			}
		}).Grow(1).Background(ui.Transparent) // the rows on the sidebar's material, as macOS draws them
		if f := a.focused(); sb.Changed() && selected != "" && (f == nil || f.path != selected && tabKey(f) != selected) {
			a.selectKey(selected)
		}
		if len(a.projects) == 0 {
			ui.Text(c, "No sessions yet. Run larik in a project, open a folder ("+keyLabel(false, "O")+"), or drop one here.").TextColor(t.TextMuted).Padding(12)
		}
		a.inbox(c, t)
	})
	switch {
	case side.FileDragOver():
		side.Background(t.Accent.Alpha(0.12))
	case !vib:
		side.Background(sidebarColor(t))
	}
	if files := side.DroppedFiles(); files != nil {
		a.dropDirs(files)
	}
}

// pinnedSection lists the pinned sessions above the projects. A session
// dragged onto it is pinned, where it's dropped; while one is dragged
// and nothing is pinned, it shows to be dropped on.
func (a *App) pinnedSection(c *ui.Context, t *ui.Theme) {
	// Dragging, as the last frame found it: the session dragged is
	// further down.
	dragging := a.dragging
	a.dragging = false
	pinned := a.pinnedSessions()
	if len(pinned) == 0 && !dragging {
		return
	}
	sec := ui.SidebarSection(c.Key("pinned"), "Pinned", nil, func() {
		for _, s := range pinned {
			_, p, _ := a.find(s.Path)
			it := a.sessionItem(c, t, s, p.Name)
			a.sessionDetail(c, t, s, p.Root)
			if d, ok := ui.Drop[sessionDrag](it); ok {
				a.movePin(string(d), s.Path)
			}
			if d, ok := ui.DragOver[sessionDrag](it); ok && string(d) != s.Path {
				it.Border(1, t.Accent)
			}
		}
		if len(pinned) == 0 {
			ui.Text(c, "Drop here to pin").FontSize(12).TextColor(t.TextMuted).Padding(6, 12)
		}
	})
	if d, ok := ui.Drop[sessionDrag](sec); ok {
		a.movePin(string(d), "")
	}
	if _, ok := ui.DragOver[sessionDrag](sec); ok {
		sec.Background(t.Accent.Alpha(0.08)).Radius(8)
	}
}

// sessionsShown is how many of a project's sessions show until asked
// for all.
const sessionsShown = 8

func (a *App) project(c *ui.Context, t *ui.Theme, p session.Project) {
	title := p.Name
	if p.Missing {
		title += " (folder gone)"
	}
	if g, ok := a.git[p.Root]; ok && g.Branch != "" {
		title += " · " + g.Branch
		if g.Dirty > 0 {
			title += fmt.Sprintf(" ±%d", g.Dirty)
		}
	}
	// Keyed, so that the Pinned section coming and going above, as a
	// drag begins, leaves the session dragged where it was.
	ui.SidebarSection(c.Key(p.Root), title, a.open[p.Root], func() {
		var sessions []session.Indexed
		hidden := 0
		for _, s := range p.Sessions {
			switch {
			case slices.Contains(a.pinned, s.Path):
				// in Pinned instead
			case a.listed(s) || a.showHidden[p.Root]:
				sessions = append(sessions, s)
			default:
				hidden++
			}
		}
		n := len(sessions)
		if !a.showAll[p.Root] {
			n = min(n, sessionsShown)
		}
		for _, s := range sessions[:n] {
			a.sessionItem(c, t, s, "")
			a.sessionDetail(c, t, s, p.Root)
		}
		if more := len(sessions) - n; more > 0 {
			if a.footLink(c, t, "more", fmt.Sprintf("Show %d more", more)) {
				a.showAll[p.Root] = true
			}
		}
		switch {
		case hidden > 0 && a.footLink(c, t, "hidden", fmt.Sprintf("Show %d hidden", hidden)):
			a.showHidden[p.Root] = true
		case a.showHidden[p.Root] && a.footLink(c, t, "hidden", "Leave hidden ones out"):
			a.showHidden[p.Root] = false
		}
		if !p.Missing {
			row := ui.Row(c.Key("new")).Gap(6).Padding(4, 12, 4, 14).Radius(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label("New session in " + p.Name).Children(func() {
				ui.Icon(c, iconPlus).FontSize(12).TextColor(t.TextMuted)
				ui.Text(c, "New session here").FontSize(12).TextColor(t.TextMuted)
			})
			if row.Hovered() {
				row.Background(t.SurfaceHover)
			}
			if row.Clicked() {
				a.newTab(p.Root, "", "")
			}
		}
	}).ContextMenu(func(m *ui.Menu) { a.projectMenu(m, p) })
}

// projectMenu is what a project's context menu offers.
func (a *App) projectMenu(m *ui.Menu, p session.Project) {
	if !p.Missing && m.Item("New Session Here").Chosen() {
		a.newTab(p.Root, "", "")
	}
	if _, err := os.Lstat(filepath.Join(p.Root, ".git")); err == nil && !p.Missing && m.Item("New Session in Worktree").Chosen() {
		a.newWorktreeSession(p.Root)
	}
	open := "Open Folder"
	if runtime.GOOS == "darwin" {
		open = "Open in Finder"
	}
	if !p.Missing && m.Item(open).Chosen() && a.openPath != nil {
		a.openPath(p.Root)
	}
	if m.Item("Copy Path").Chosen() && a.copyText != nil {
		a.copyText(p.Root)
	}
}

// footLink is a quiet link under a project's sessions, reporting a click.
func (a *App) footLink(c *ui.Context, t *ui.Theme, key, text string) bool {
	return ui.Text(c.Key(key), text).FontSize(12).TextColor(t.TextMuted).Padding(4, 12, 4, 34).Cursor(ui.CursorPointer).Clicked()
}

// resultsShown is how many sessions a search shows.
const resultsShown = 50

// results lists what the search finds, across every project, each with
// its project.
func (a *App) results(c *ui.Context, t *ui.Theme) {
	a.searchConversations()
	found := a.matches()
	title := fmt.Sprintf("%d found", len(found))
	if len(found) == 0 {
		title = "Nothing found"
	}
	ui.SidebarSection(c, title, nil, func() {
		for _, s := range found[:min(len(found), resultsShown)] {
			_, p, _ := a.find(s.Path)
			a.sessionItem(c, t, s, p.Name)
			a.sessionDetail(c, t, s, p.Root)
			if said := a.saidIn(s.Path); said != "" {
				ui.Text(c, said).FontSize(11).TextColor(t.TextMuted).MaxLines(2).Padding(0, 12, 4, 34)
			}
		}
		if len(found) > resultsShown {
			ui.Text(c, fmt.Sprintf("and %d more", len(found)-resultsShown)).FontSize(12).TextColor(t.TextMuted).Padding(4, 12, 4, 34)
		}
	})
}

// openPicked opens the chosen match of the search and ends the search,
// showing the session in its project.
func (a *App) openPicked() {
	found := a.matches()
	if len(found) == 0 {
		return
	}
	path := found[min(a.pick, len(found)-1)].Path
	a.query, a.pick = "", 0
	a.reveal(path)
}

// movePick moves the chosen match by, within those the results show.
func (a *App) movePick(by int) {
	if n := min(len(a.matches()), resultsShown); n > 0 {
		a.pick = min(max(a.pick+by, 0), n-1)
	}
}

// sessionItem is a session in the sidebar, with its status marker, the
// project when it shows outside it, and a menu of what else to do.
func (a *App) sessionItem(c *ui.Context, t *ui.Theme, s session.Indexed, project string) ui.Element {
	if a.renaming == s.Path {
		return a.renameRow(c, t, s)
	}
	label := s.Title
	if label == "" {
		label = "Untitled session"
	}
	tip := label
	if s.Subdir != "" {
		tip = s.Subdir + ": " + label
	}
	if project != "" {
		tip = project + " · " + tip
	}
	icon := iconChat
	if a.tabOf(s.Path) != nil {
		icon = iconTerm
	}
	if prompt := a.prompts[s.Path]; prompt != "" && prompt != s.Title {
		tip += "\n" + prompt
	}
	if a.hidden[s.Path] {
		tip += " (hidden)"
	}
	item := ui.SidebarItem(c, s.Path, icon, label).Tooltip(tip)
	if a.hidden[s.Path] {
		item.Opacity(0.55)
	}
	// Dragged onto a pane, or the split button, it opens there.
	item.Drag(sessionDrag(s.Path))
	if item.Dragging() {
		a.dragging = true
	}
	item.Children(func() {
		if project != "" {
			ui.Text(c, project).FontSize(11).TextColor(t.TextMuted).SingleLine().MaxWidth(90)
		}
		if !a.marker(c, t, s.Status, a.unread(s)) {
			ui.Text(c, age(time.Since(s.Modified))).FontSize(11).TextColor(t.TextMuted).SingleLine().Tooltip(s.Modified.Format("Mon 2 Jan 2006, 15:04"))
		}
	}).ContextMenu(func(m *ui.Menu) { a.sessionMenu(m, s) })
	return item
}

// sessionDetail shows under a session's row what it is doing, while it
// works or waits for you; the branch of the worktree it runs in, when
// that isn't its project's; and the ports its programs listen on, each a
// link that opens it in the browser.
func (a *App) sessionDetail(c *ui.Context, t *ui.Theme, s session.Indexed, root string) {
	defer a.answerButtons(c, t, s, 34)
	var line []string
	if st := s.Status.State; (st == status.Working || st == status.Blocked) && s.Status.Msg != "" {
		line = append(line, s.Status.Msg)
	}
	if g, ok := a.git[s.Cwd]; ok && s.Cwd != root && g.Branch != "" && g.Branch != a.git[root].Branch {
		line = append(line, g.Branch)
	}
	ports := a.ports[s.Path]
	if len(line) == 0 && len(ports) == 0 {
		return
	}
	ui.Row(c.Key("detail-"+s.Path)).Gap(8).Padding(0, 12, 4, 34).AlignItems(ui.Center).Children(func() {
		if len(line) > 0 {
			ui.Text(c, strings.Join(line, " · ")).FontSize(11).TextColor(t.TextMuted).SingleLine().Grow(1).Tooltip(strings.Join(line, "\n"))
		}
		for _, port := range ports {
			url := fmt.Sprintf("http://localhost:%d", port)
			link := ui.Text(c.Key(url), fmt.Sprintf(":%d", port)).FontSize(11).TextColor(t.Accent).Cursor(ui.CursorPointer).Tooltip("Open " + url).Label("Open " + url)
			if link.Clicked() && a.openExternal != nil {
				a.openExternal(url)
			}
		}
	})
}

// renameRow is a session's row while it's renamed in place, as Finder
// renames: Enter, or clicking away, names it; Escape leaves it as it
// was; a blank name gives it its first prompt back.
func (a *App) renameRow(c *ui.Context, t *ui.Theme, s session.Indexed) ui.Element {
	return ui.Row(c.Key("rename")).Gap(6).Padding(2, 4, 2, 10).AlignItems(ui.Center).Children(func() {
		ui.Icon(c, iconChat).FontSize(14).TextColor(t.Accent)
		field := ui.TextInput(c.Key("name"), &a.newName).Label("Session name").Placeholder(a.prompts[s.Path]).Grow(1).
			OnSubmit(func() { a.endRename(true) }).
			OnShortcut(0, ui.KeyEscape, func() { a.endRename(false) })
		switch {
		case a.focusRename:
			// The whole name selected, to type over, as Finder starts.
			field.Focus().SetTextSelection(0, utf8.RuneCountInString(a.newName))
			a.focusRename, a.renameFocused = false, false
		case field.Focused():
			a.renameFocused = true
		case a.renameFocused:
			a.endRename(true) // the keyboard went elsewhere
		}
	})
}

// sessionMenu is what a session's context menu offers.
func (a *App) sessionMenu(m *ui.Menu, s session.Indexed) {
	a.answerMenu(m, s)
	if m.Item("Open").Chosen() {
		a.selectSession(s.Path)
	}
	if m.Item("Open in Split").Chosen() {
		a.openInSplit(s.Path)
	}
	if m.Item("Fork into a New Tab").Chosen() {
		a.fork(s.Path)
	}
	if m.Item("Rename…").Chosen() {
		a.askRename(s.Path)
	}
	if slices.Contains(a.pinned, s.Path) {
		if m.Item("Unpin").Chosen() {
			a.pin(s.Path, false)
		}
	} else if m.Item("Pin to Top").Chosen() {
		a.pin(s.Path, true)
	}
	if a.hidden[s.Path] {
		if m.Item("Show in Sidebar").Chosen() {
			delete(a.hidden, s.Path)
		}
	} else if m.Item("Hide from Sidebar").Chosen() {
		a.hidden[s.Path] = true
		a.pin(s.Path, false)
	}
	if _, p, ok := a.find(s.Path); ok {
		a.worktreeMenu(m, s, p.Root)
	}
	m.Separator()
	if m.Item("Copy Session ID").Chosen() && a.copyText != nil {
		a.copyText(s.ID)
	}
	if m.Item(revealLabel()).Chosen() && a.showFile != nil {
		a.showFile(s.Path)
	}
}

func revealLabel() string {
	if runtime.GOOS == "darwin" {
		return "Show Transcript in Finder"
	}
	return "Show Transcript in Folder"
}

// marker shows a session's status beside it: a spinner while it works,
// a pause sign while it waits for you, and for what finished or failed
// since you looked, a check or a cross. It reports whether it showed one.
func (a *App) marker(c *ui.Context, t *ui.Theme, st status.Report, unread bool) bool {
	switch {
	case st.State == status.Working:
		ui.Spinner(c).Size(12, 12).Label("Working")
	case st.State == status.Blocked:
		ui.Icon(c, iconPause).FontSize(13).TextColor(t.Warning).Tooltip(st.Msg).Label("Needs you")
	case st.State == status.Error && unread:
		ui.Icon(c, iconError).FontSize(13).TextColor(t.Danger).Label("Failed")
	case st.State == status.Done && unread:
		ui.Icon(c, iconCheck).FontSize(13).TextColor(t.Success).Label("Done")
	default:
		return false
	}
	return true
}

// age is how long ago something was, as briefly as the sidebar wants it:
// now, 5m, 3h, 2d, 4w, then 1y.
func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dw", int(d/(7*24*time.Hour)))
	}
	return fmt.Sprintf("%dy", int(d/(365*24*time.Hour)))
}

// inbox counts what needs attention across every project; a click opens
// the newest of each.
func (a *App) inbox(c *ui.Context, t *ui.Theme) {
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
	ui.Row(c).Gap(6).Padding(8, 6, 0, 6).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, "Inbox").Bold().FontSize(11).TextColor(t.TextMuted)
		ui.Spacer(c)
		if len(blocked)+len(failed)+len(done) == 0 {
			ui.Text(c, "all clear").FontSize(12).TextColor(t.TextMuted)
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
			{"done", done, iconCheck, t.Success, "done"},
		} {
			if len(g.paths) == 0 {
				continue
			}
			chip := ui.Row(c.Key(g.key)).Gap(3).Padding(2, 6).Radius(999).Cursor(ui.CursorPointer).Tooltip(fmt.Sprintf("%d %s", len(g.paths), g.label)).Children(func() {
				ui.Icon(c, g.icon).FontSize(12).TextColor(g.color)
				ui.Text(c, fmt.Sprint(len(g.paths))).FontSize(12)
			})
			if chip.Hovered() {
				chip.Background(t.SurfaceHover)
			}
			if chip.Clicked() {
				a.reveal(g.paths[0])
			}
		}
	})
}

// reveal opens the session at path and its project's section.
func (a *App) reveal(path string) {
	if _, p, ok := a.find(path); ok {
		*a.open[p.Root] = true
	}
	a.selectSession(path)
}

// main is the open tab's card, with the split or the changes beside it,
// or a hint when no tab is open.
func (a *App) main(c *ui.Context, t *ui.Theme) {
	if a.active == nil {
		a.card(c, t, func() {
			a.noticeBar(c, t)
			empty := ui.Column(c).Fill().Center().Gap(6).Children(func() {
				ui.Text(c, "No Session Open").Bold().FontSize(17).TextColor(t.TextMuted)
				ui.Text(c, "Pick a session, or start a new one").TextColor(t.TextMuted)
				ui.Box(c).Height(6)
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c.Key("start"), "New session").Clicked() {
						a.newTab(a.currentDir(), "", "")
					}
					if ui.Button(c.Key("shell"), "New terminal").Clicked() {
						a.openShell()
					}
				})
			})
			if d, ok := ui.Drop[sessionDrag](empty); ok {
				a.dropSession(string(d), false)
			}
			if _, ok := ui.DragOver[sessionDrag](empty); ok {
				empty.Background(t.Accent.Alpha(0.08))
			}
		})
		return
	}
	// The changes are the main tab's project's: they follow it.
	if a.changes != nil && a.changes.dir != a.active.dir {
		a.closeChanges()
		a.toggleChanges()
	}
	left := func() {
		a.card(c, t, func() {
			a.noticeBar(c, t)
			a.pane(c, t, a.active, false)
		})
	}
	switch {
	case a.changes != nil:
		ui.SplitPercent(c, &a.splitP, left, func() { a.changesPane(c, t, a.changes) }).Grow(1)
	case a.right != nil:
		ui.SplitPercent(c, &a.splitP, left, func() {
			a.card(c, t, func() { a.pane(c, t, a.right, true) })
		}).Grow(1)
	default:
		ui.Column(c).Grow(1).Children(left)
	}
}

// card is a pane: flush with the window's edges and its neighbour, as
// macOS's split views are.
func (a *App) card(c *ui.Context, t *ui.Theme, fn func()) {
	ui.Column(c).Fill().Background(t.Background).Clip().Children(fn)
}

func (a *App) noticeBar(c *ui.Context, t *ui.Theme) {
	if a.notice == "" {
		return
	}
	ui.Row(c).Gap(8).Padding(8, 14).AlignItems(ui.Center).Background(t.Warning.Alpha(0.1)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Text(c, a.notice).FontSize(12).Grow(1).Shrink(1)
		a.closeButton(c, t, "Dismiss", func() { a.notice = "" })
	})
}

// pane is a tab's header, find bar and terminal.
func (a *App) pane(c *ui.Context, t *ui.Theme, tb *tab, right bool) {
	var term ui.Element
	body := ui.Column(c).Grow(1).Children(func() {
		a.header(c, t, tb, right)
		a.exitBar(c, t, tb)
		if tb.finding {
			a.findBar(c, t, tb)
		}
		term = terminal.View(c.Key(tb.id), tb.term).Grow(1)
	})
	if d, ok := ui.Drop[sessionDrag](body); ok {
		a.dropSession(string(d), right)
	}
	if _, ok := ui.DragOver[sessionDrag](body); ok {
		body.Border(2, t.Accent)
	} else if a.needsLook(tb) {
		body.Border(2, a.ringColor(t, tb))
	}
	// The keyboard anywhere in the pane, its find bar too, makes its tab
	// the focused one.
	if a.focusNext == tb {
		term.Focus()
		a.focusNext = nil
	} else if body.FocusWithin() && a.right != nil && a.rightFocused != right {
		a.rightFocused = right
	}
}

// exitBar says a tab's larik failed, over what it printed, and offers
// to run it again.
func (a *App) exitBar(c *ui.Context, t *ui.Theme, tb *tab) {
	if !tb.exited || tb.code == 0 {
		return
	}
	ui.Row(c.Key("exit")).Gap(8).Padding(6, 10, 6, 16).AlignItems(ui.Center).Background(t.Danger.Alpha(0.08)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Icon(c, iconError).FontSize(13).TextColor(t.Danger)
		what := "larik exited with status " + strconv.Itoa(tb.code)
		if tb.path != "" {
			what += "; restarting resumes the session"
		}
		ui.Text(c, what).FontSize(12).Grow(1).Shrink(1)
		if ui.Button(c.Key("restart"), "Restart").Clicked() {
			a.restart(tb)
		}
	})
}

// findBar finds text in a tab's terminal, its screen and scrollback:
// Enter goes to the next match up, Shift+Enter back down, and Escape
// clears the text, then closes the bar.
func (a *App) findBar(c *ui.Context, t *ui.Theme, tb *tab) {
	ui.Row(c.Key("find")).Gap(6).Padding(6, 10, 6, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		field := ui.SearchField(c.Key("field"), &tb.find).Placeholder("Find").Label("Find in the terminal").Grow(1).MaxWidth(320).
			OnChange(func() { tb.term.Search(tb.find) }).
			OnSubmit(tb.term.SearchNext).
			OnShortcut(ui.Shift, ui.KeyEnter, tb.term.SearchPrevious).
			OnShortcut(0, ui.KeyEscape, func() { a.closeFind(tb) })
		if tb.focusFind {
			field.Focus()
			tb.focusFind = false
		}
		var count string
		switch st := tb.search; {
		case tb.find == "":
		case st.Total == 0 && !st.Searching:
			count = "No matches"
		case st.Selected > 0:
			count = fmt.Sprintf("%d of %d", st.Selected, st.Total)
		default:
			count = fmt.Sprintf("%d found", st.Total)
		}
		ui.Text(c, count).FontSize(12).TextColor(t.TextMuted).SingleLine()
		ui.Spacer(c)
		a.iconButton(c, t, "older", iconUp, "Next match up (Enter)", false, tb.term.SearchNext)
		a.iconButton(c, t, "newer", iconDown, "Next match down (Shift+Enter)", false, tb.term.SearchPrevious)
		a.closeButton(c, t, "Close find", func() { a.closeFind(tb) })
	})
}

// header names each pane's session while the window is split, the
// focused one's in bold, with a button to close the tab or the split. The
// toolbar names the session otherwise.
func (a *App) header(c *ui.Context, t *ui.Theme, tb *tab, right bool) {
	if a.right == nil {
		return
	}
	ui.Row(c).Height(28).Gap(6).Padding(0, 6, 0, 12).AlignItems(ui.Center).Background(t.Text.Alpha(0.03)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		statusDot(c, t, a.tabStatus(tb))
		title := ui.Text(c, a.tabTitle(tb)).FontSize(12).SingleLine().Shrink(1)
		if tb == a.focused() {
			title.Bold()
		}
		ui.Text(c, a.where(tb)).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		if right {
			a.closeButton(c, t, "Close split (the tab stays open)", a.unsplit)
		} else {
			a.closeButton(c, t, "Close tab", func() { a.askClose([]*tab{tb}, nil) })
		}
	})
}

func (a *App) iconButton(c *ui.Context, t *ui.Theme, key string, icon *ui.SVG, tip string, on bool, fn func()) ui.Element {
	// As macOS's toolbar buttons: borderless, a gray face while pressed
	// in or hovered.
	b := ui.Box(c.Key(key)).Padding(6).Radius(6).Label(tip).Tooltip(tip).Children(func() {
		ui.Icon(c, icon).FontSize(15).TextColor(t.Text.Alpha(0.75))
	})
	switch {
	case on:
		b.Background(t.Text.Alpha(0.1))
	case b.Hovered():
		b.Background(t.Text.Alpha(0.06))
	}
	if b.Clicked() {
		fn()
	}
	return b
}

func (a *App) closeButton(c *ui.Context, t *ui.Theme, label string, fn func()) {
	b := ui.Box(c.Key("close")).Padding(4).Radius(5).Label(label).Tooltip(label).Children(func() {
		ui.Icon(c, iconClose).FontSize(12).TextColor(t.TextMuted)
	})
	if b.Hovered() {
		b.Background(t.Text.Alpha(0.06))
	}
	if b.Clicked() {
		fn()
	}
}

// keyLabel is how a window shortcut reads in a tooltip. Off macOS the
// shortcuts take Control+Shift whether or not they add Shift.
func keyLabel(shift bool, key string) string {
	if runtime.GOOS != "darwin" {
		return "Ctrl+Shift+" + key
	}
	if shift {
		return "⇧⌘" + key
	}
	return "⌘" + key
}
