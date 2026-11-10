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

	"github.com/egoist/mygo"
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
	iconChat       = svg(`<path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/>`)
	iconTerm       = svg(`<path d="m4 17 6-6-6-6M12 19h8"/>`)
	iconBranch     = svg(`<circle cx="6" cy="3" r="2"/><path d="M6 5v12a4 4 0 0 0 4 4h4"/><circle cx="6" cy="19" r="2"/><circle cx="18" cy="6" r="2"/><path d="M18 8v5a4 4 0 0 1-4 4H8"/>`)
	iconPlus       = svg(`<path d="M12 5v14M5 12h14"/>`)
	iconClose      = svg(`<path d="M18 6 6 18M6 6l12 12"/>`)
	iconPause      = svg(`<circle cx="12" cy="12" r="10"/><path d="M10 15V9M14 15V9"/>`)
	iconCheck      = svg(`<path d="M20 6 9 17l-5-5"/>`)
	iconError      = svg(`<circle cx="12" cy="12" r="10"/><path d="m15 9-6 6M9 9l6 6"/>`)
	iconDiff       = svg(`<path d="M12 3v14M5 10h14M5 21h14"/>`)
	iconSplit      = svg(`<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M12 3v18"/>`)
	iconPanel      = svg(`<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M9 3v18"/>`)
	iconPanelRight = svg(`<rect width="18" height="18" x="3" y="3" rx="2"/><path d="M15 3v18"/>`)
	iconUp         = svg(`<path d="m18 15-6-6-6 6"/>`)
	iconDown       = svg(`<path d="m6 9 6 6 6-6"/>`)
	iconRight      = svg(`<path d="m9 18 6-6-6-6"/>`)
	iconZoom       = svg(`<path d="M15 3h6v6M9 21H3v-6M21 3l-7 7M3 21l7-7"/>`)
	iconShrink     = svg(`<path d="M4 14h6v6M20 10h-6V4M14 10l7-7M3 21l7-7"/>`)
	iconDetach     = svg(`<rect x="3" y="3" width="14" height="14" rx="2"/><path d="m11 11 10 10m-6 0h6v-6"/>`)
	iconGlobe      = svg(`<circle cx="12" cy="12" r="10"/><path d="M12 2a14.5 14.5 0 0 0 0 20 14.5 14.5 0 0 0 0-20M2 12h20"/>`)
	iconStar       = svg(`<path d="m12 2 3.1 6.3 7 1-5 4.9 1.2 7-6.3-3.3-6.3 3.3 1.2-7-5-4.9 7-1z"/>`)
	iconStarFilled = svg(`<path fill="currentColor" d="m12 2 3.1 6.3 7 1-5 4.9 1.2 7-6.3-3.3-6.3 3.3 1.2-7-5-4.9 7-1z"/>`)
	iconFolder     = svg(`<path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>`)
	iconDatabase   = svg(`<ellipse cx="12" cy="5" rx="9" ry="3"/><path d="M3 5v14c0 1.7 4 3 9 3s9-1.3 9-3V5M3 12c0 1.7 4 3 9 3s9-1.3 9-3"/>`)
	iconFilter     = svg(`<path d="M4 7h16M7 12h10M10 17h4"/>`)
	iconWrap       = svg(`<path d="M4 6h16M4 11h16M4 16h10M17 16h2a2 2 0 0 0 0-4h-2m0 4 2-2m-2 2 2 2"/>`)
	iconBack       = svg(`<path d="m15 18-6-6 6-6"/>`)
	iconForward    = svg(`<path d="m9 18 6-6-6-6"/>`)
	iconCopy       = svg(`<rect x="8" y="8" width="13" height="13" rx="2"/><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h3"/>`)
	iconExport     = svg(`<path d="M12 3v12m-4-4 4 4 4-4M4 17v3a1 1 0 0 0 1 1h14a1 1 0 0 0 1-1v-3"/>`)
	iconRun        = svg(`<path d="m8 5 11 7-11 7V5z"/>`)
	iconReload     = svg(`<path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8"/><path d="M21 3v5h-5"/>`)
	iconExternal   = svg(`<path d="M15 3h6v6M10 14 21 3M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/>`)
	iconBell       = svg(`<path d="M6 8a6 6 0 0 1 12 0c0 7 3 9 3 9H3s3-2 3-9"/><path d="M10.3 21a1.94 1.94 0 0 0 3.4 0"/>`)
	iconArchive    = svg(`<rect width="20" height="5" x="2" y="3" rx="1"/><path d="M4 8v11a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8M10 12h4"/>`)
	iconTrash      = svg(`<path d="M3 6h18M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6M8 6V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>`)
	iconLaptop     = svg(`<rect width="18" height="12" x="3" y="4" rx="2"/><path d="M2 20h20"/>`)
	iconServer     = svg(`<rect width="20" height="8" x="2" y="2" rx="2"/><rect width="20" height="8" x="2" y="14" rx="2"/><path d="M6 6h.01M6 18h.01"/>`)
)

// macOS's colours: the content's background and labels, the sidebar
// where the window shows no material, the tab bar behind the tabs not
// shown, and selected text.
var (
	contentLight, contentDark   = ui.Hex("#ffffff"), ui.Hex("#1e1e1e")
	labelLight, labelDark       = ui.Hex("#262626"), ui.Hex("#dfdfdf")
	sidebarLight, sidebarDark   = ui.Hex("#ebebeb"), ui.Hex("#2a2a2a")
	selectedLight, selectedDark = ui.Hex("#b3d7ff"), ui.Hex("#3f638b")
)

// palette is the window's theme: macOS's colours, on the system's theme,
// which has the accent colour chosen in System Settings.
func (a *App) palette(base *ui.Theme) *ui.Theme {
	i := 0
	if base.Dark {
		i = 1
	}
	want := base.Accent
	if a.accent != "" {
		want = ui.Hex(a.accent)
	}
	if t := a.theme[i]; t != nil && t.Accent == want && t.FontSize == base.FontSize {
		return t
	}
	t := *base
	if a.accent != "" {
		accented(&t, a.accent)
	}
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
	// toolbar and the panes.
	vib := c.Vibrancy()
	seeThrough := a.paneOpacity < 1
	if vib || seeThrough {
		c.Root().Background(ui.Transparent)
	}
	shell := ui.Themed(c, t).Fill()
	if vib || seeThrough {
		shell.Background(ui.Transparent) // the material shows through the sidebar
	}
	shell.Children(func() {
		content := func() {
			background := t.Background
			if seeThrough {
				background = ui.Transparent
			}
			ui.Column(c).Fill().Background(background).Children(func() {
				a.toolbar(c, t)
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
		a.filesEditDialog(c)
		a.filesMoveDialog(c)
		a.filesConflictDialog(c)
		a.remoteFolderDialog(c)
		a.layoutDialog(c)
		a.workspaceRenameDialog(c)
		a.recentDialog(c)
		a.commandPalette(c, t)
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

// confirmClose asks before closing a working session or live terminal.
func (a *App) confirmClose(c *ui.Context) {
	var title, msg string
	if a.confirming {
		working := a.closingWorking()
		terminals := a.closingTerminals()
		if len(a.closing) == 1 {
			title = fmt.Sprintf("Close “%s”?", a.tabTitle(a.closing[0]))
			if len(terminals) > 0 {
				msg = "This terminal is still running. Closing it ends its shell or command."
			} else {
				msg = "Larik is working in this pane. Closing it stops the turn; the session stays, so you can resume it."
			}
		} else {
			title = fmt.Sprintf("Close %d panes?", len(a.closing))
			msg = fmt.Sprintf("Closing ends %d running terminals and stops %d working sessions. The sessions stay, so you can resume them.", len(terminals), len(working))
			if len(terminals) == 0 {
				msg = fmt.Sprintf("%d of them are working. Closing stops their turns; the sessions stay, so you can resume them.", len(working))
			}
			if len(terminals) == 0 && len(working) == 1 {
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
		stopping := a.stopping()
		var terminals, sessions int
		for _, tb := range stopping {
			if tb.shell() {
				terminals++
			} else {
				sessions++
			}
		}
		msg = fmt.Sprintf("Leaving ends %d running terminals and stops %d working sessions. The sessions stay and their panes open again next time.", terminals, sessions)
		if terminals == 0 {
			msg = fmt.Sprintf("%d sessions are working. Leaving stops their turns; the sessions stay, and their panes open again next time.", sessions)
			if sessions == 1 {
				msg = fmt.Sprintf("“%s” is working. Leaving stops its turn; the session stays, and its pane opens again next time.", a.tabTitle(stopping[0]))
			}
		} else if terminals == 1 && sessions == 0 {
			msg = fmt.Sprintf("“%s” is still running. Leaving ends its shell or command; the pane opens again next time.", a.tabTitle(stopping[0]))
		}
	}
	leave := func(yes bool) {
		if !yes {
			a.quitting, a.ending = false, false
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
	a.customShortcuts(c)
	a.tilingShortcuts(c)
	mod := ui.Ctrl | ui.Shift
	if runtime.GOOS == "darwin" {
		mod = ui.Super
	}
	switch {
	case a.focused() != nil && a.focused().browser && c.Shortcut(ui.Ctrl, ui.KeyTab):
		a.cycleBrowserTab(1)
	case a.focused() != nil && a.focused().browser && c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyTab):
		a.cycleBrowserTab(-1)
	case a.keyed(c, paletteLabel, mod|ui.Shift, ui.KeyP):
		a.openPalette()
	case a.keyed(c, "New Session in Worktree", mod|ui.Alt, ui.KeyT):
		a.newWorktreeSession(a.currentDir())
	case a.keyed(c, "New Pane", mod, ui.KeyT):
		if t := a.focused(); t != nil && t.browser {
			a.newBrowserTab()
		} else {
			a.openNew()
		}
	case a.keyed(c, "Close Pane", mod, ui.KeyW):
		if t := a.focused(); t != nil {
			a.askClose([]*tab{t}, nil)
		}
	case a.keyed(c, "New Pane Below", splitBelowMods(), ui.KeyD):
		a.splitNew(true)
	case a.keyed(c, "New Pane Beside", mod, ui.KeyD):
		a.splitRight()
	case a.keyed(c, "Zoom Pane", mod|ui.Shift, ui.KeyEnter):
		a.toggleZoom()
	case a.keyed(c, "Pane to the Left", mod|ui.Alt, ui.KeyLeft):
		a.focusToward(-1, 0)
	case a.keyed(c, "Pane to the Right", mod|ui.Alt, ui.KeyRight):
		a.focusToward(1, 0)
	case a.keyed(c, "Pane Above", mod|ui.Alt, ui.KeyUp):
		a.focusToward(0, -1)
	case a.keyed(c, "Pane Below", mod|ui.Alt, ui.KeyDown):
		a.focusToward(0, 1)
	case a.keyed(c, "Previous Focused Pane", mod|ui.Alt, ui.KeyBracketLeft):
		a.navigateFocus(-1)
	case a.keyed(c, "Next Focused Pane", mod|ui.Alt, ui.KeyBracketRight):
		a.navigateFocus(1)
	case a.keyed(c, "Open Folder…", mod, ui.KeyO):
		a.openFolder()
	case a.keyed(c, "Settings…", mod, ui.KeyComma):
		a.openSettings()
	case a.keyed(c, "Toggle Changes", mod|ui.Shift, ui.KeyG):
		a.toggleChanges()
	case a.keyed(c, "Next Session Waiting", mod, ui.KeyJ):
		a.nextWaiting()
	case a.keyed(c, "Latest Notification", mod|ui.Shift, ui.KeyU):
		a.latestUnread()
	case a.keyed(c, "New Terminal", mod|ui.Shift, ui.KeyN):
		a.openShell()
	case a.keyed(c, "New Browser Pane", mod|ui.Alt, ui.KeyN):
		a.openBrowser("")
	case a.keyed(c, "Open Location…", mod, ui.KeyL):
		a.focusAddress()
	case a.keyed(c, "Refresh Pane", mod, ui.KeyR):
		a.refreshFocused()
	case a.keyed(c, "Search Sessions…", mod, ui.KeyK):
		a.focusSearch, a.noSidebar = true, false
	case a.keyed(c, "Find…", mod, ui.KeyF):
		a.openFind()
	case a.keyed(c, "Toggle Sidebar", mod, ui.KeyB):
		a.noSidebar = !a.noSidebar
	case a.keyed(c, "Focus Next Pane", mod|ui.Shift, ui.KeyBracketRight):
		a.cycle(1)
	case a.keyed(c, "Focus Previous Pane", mod|ui.Shift, ui.KeyBracketLeft):
		a.cycle(-1)
	case runtime.GOOS == "darwin" && a.keyed(c, "Focus Next Window", ui.Super, ui.KeyBackquote):
		if a.navigateWindow != nil {
			a.navigateWindow(1)
		}
	case runtime.GOOS == "darwin" && a.keyed(c, "Focus Previous Window", ui.Super|ui.Shift, ui.KeyBackquote):
		if a.navigateWindow != nil {
			a.navigateWindow(-1)
		}
	case runtime.GOOS != "darwin" && a.keyed(c, "Focus Next Window", ui.Ctrl|ui.Alt|ui.Shift, ui.KeyPageDown):
		if a.navigateWindow != nil {
			a.navigateWindow(1)
		}
	case runtime.GOOS != "darwin" && a.keyed(c, "Focus Previous Window", ui.Ctrl|ui.Alt|ui.Shift, ui.KeyPageUp):
		if a.navigateWindow != nil {
			a.navigateWindow(-1)
		}
	}
	// Off macOS the shortcuts take Shift already: there, the tab menu
	// reopens a tab.
	if runtime.GOOS == "darwin" && a.keyed(c, "Reopen Closed Pane", ui.Super|ui.Shift, ui.KeyT) {
		a.reopenClosed()
	}
	// And a browser tab's back and forward, which differ from showing
	// the next tab by Shift alone.
	if runtime.GOOS == "darwin" && a.keyed(c, "Back", ui.Super, ui.KeyBracketLeft) {
		a.browse((*mygo.Page).GoBack)
	}
	if runtime.GOOS == "darwin" && a.keyed(c, "Forward", ui.Super, ui.KeyBracketRight) {
		a.browse((*mygo.Page).GoForward)
	}
	for i, k := range []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9} {
		if c.Shortcut(mod, k) && len(a.tabs) > 0 {
			if current := a.focused(); current != nil && current.browser {
				var pages []*tab
				for _, page := range a.tabs {
					if page.browser {
						pages = append(pages, page)
					}
				}
				if len(pages) > 0 {
					if p := a.paneOf(current); p != nil {
						p.tab = pages[min(i, len(pages)-1)]
						a.focusPane(p)
					}
				}
			} else {
				a.activate(a.tabs[min(i, len(a.tabs)-1)])
			}
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

// trafficLights is where the close button's top-left corner goes: the
// 14-point buttons centered in the toolbar's height, as far from the
// window's left edge as from its top, as Finder's.
var trafficLights = &mygo.Point{X: 20, Y: (toolbarHeight - 14) / 2}

// toolbar is the content's title bar, as macOS draws a unified toolbar:
// the focused session's title and where it is, and buttons to split,
// show the changes, and start a session. It moves the window.
func (a *App) toolbar(c *ui.Context, t *ui.Theme) {
	bar := c.TitleBar()
	start := float32(16)
	if a.noSidebar {
		start = bar.Left + 10
	}
	ui.Row(c).Height(max(bar.Height, toolbarHeight)).PaddingStart(start).PaddingEnd(bar.Right+10).Gap(4).AlignItems(ui.Center).Background(t.Background).
		BorderWidth(0, 0, 1, 0).BorderColor(t.Border).DragWindow().Children(func() {
		if a.noSidebar {
			a.sidebarButton(c, t)
			ui.Box(c).Width(6)
		}
		title, where := "Workspace", "0 panes"
		f := a.focused()
		if f != nil {
			where = fmt.Sprintf("%d pane", len(a.panes()))
			if a.split() {
				where = fmt.Sprintf("%d panes", len(a.panes()))
			}
		}
		if a.windowPosition != nil {
			if index, total := a.windowPosition(); total > 1 {
				title = fmt.Sprintf("Window %d of %d", index, total)
			}
		}
		contentWidth := 1240
		if a.contentSize != nil {
			contentWidth, _ = a.contentSize()
		}
		if !a.noSidebar {
			contentWidth -= int(a.sidebarW)
		}
		if contentWidth >= 650 {
			ui.Column(c).Gap(1).Shrink(1).MinWidth(0).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, title).Bold().FontSize(13).SingleLine().Shrink(1)
				})
				sub := ui.Text(c, where).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
				if f != nil {
					sub.Tooltip(tildePath(f.dir))
				}
			})
			ui.Box(c).Width(8)
		}
		a.workspaceTabs(c, t)
		ui.Spacer(c)
		if a.layout != nil {
			split := a.iconButton(c, t, "split", iconSplit, splitTip(), false, a.splitRight)
			split.ContextMenu(func(m *ui.Menu) { a.splitMenu(m) })
			options := ui.Row(c.Key("split-options")).Padding(5, 4).Radius(6).AlignItems(ui.Center).
				Label("Pane options").Tooltip("Open a new pane or arrange panes").Children(func() {
				ui.Icon(c, iconDown).FontSize(11).TextColor(t.TextMuted)
			})
			options.Menu(func(m *ui.Menu) { a.splitMenu(m) })
			if options.Hovered() {
				options.Background(t.Text.Alpha(0.06))
			}
			if a.zoomed {
				a.iconButton(c, t, "show-all-panes", iconShrink, "Show All Panes ("+keyText("Shift+Cmd+Enter")+")", false, a.toggleZoom)
			}
			if d, ok := ui.Drop[sessionDrag](split); ok {
				a.openInSplit(string(d), false)
			}
			if _, ok := ui.DragOver[sessionDrag](split); ok {
				split.Background(t.Accent.Alpha(0.25))
			}
			a.iconButton(c, t, "changes", iconDiff, "Changes in this project ("+keyLabel(true, "G")+")", a.changes != nil, a.toggleChanges)
		}
		a.bell(c, t)
		newButton := ui.Row(c.Key("new")).Gap(3).Padding(5, 7).Radius(6).AlignItems(ui.Center).
			Label("New pane").Tooltip("New session, worktree session, terminal, browser, Files, or Database pane").Children(func() {
			ui.Icon(c, iconPlus).FontSize(15).TextColor(t.Text.Alpha(0.75))
			ui.Text(c, "New").FontSize(12)
			ui.Icon(c, iconDown).FontSize(10).TextColor(t.TextMuted)
		})
		if newButton.Hovered() {
			newButton.Background(t.Text.Alpha(0.06))
		}
		newButton.Menu(func(m *ui.Menu) { a.creationMenu(m, nil) })
	})
}

func (a *App) splitMenu(m *ui.Menu) {
	if m.Item("New Pane Beside (" + keyLabel(false, "D") + ")").Chosen() {
		a.splitNew(false)
	}
	if m.Item("New Pane Below (" + keyText(splitBelowKey()) + ")").Chosen() {
		a.splitNew(true)
	}
	if a.split() {
		for _, cmd := range a.tilingCommands() {
			if cmd.label == "Equalize Panes" || cmd.label == "Move Pane Left" || cmd.label == "Undo Pane Layout" {
				m.Separator()
			}
			if m.Item(cmd.label).Chosen() {
				cmd.run()
			}
		}
	}
}

// creationMenu offers the four kinds of tab in the focused pane, or in
// p when opened from a pane header. The old tab stays open.
func (a *App) creationMenu(m *ui.Menu, p *pane) {
	inPane := func() {
		if p != nil && a.paneOf(p.tab) == p {
			a.focusPane(p)
		}
	}
	if m.Item("New Larik Session").Chosen() {
		inPane()
		a.newTab(a.currentDir(), "", "")
	}
	if m.Item("New Session in Worktree").Chosen() {
		inPane()
		a.newWorktreeSession(a.currentDir())
	}
	if m.Item("New Terminal").Chosen() {
		inPane()
		a.openShell()
	}
	if m.Item("New Browser Pane").Chosen() {
		inPane()
		a.openBrowser("")
	}
	if m.Item("New Files Pane").Chosen() {
		inPane()
		a.openFiles(a.currentDir())
	}
	if m.Item("New Database Pane").Chosen() {
		inPane()
		a.openDatabase(a.currentDir())
	}
}

// where says where a tab's session is and what it's doing: its project
// and folder, its status, and its model.
func (a *App) where(tb *tab) string {
	if tb.files != nil {
		return tildePath(tb.dir)
	}
	if tb.database != nil {
		return tildePath(tb.dir)
	}
	if tb.chooser {
		return tildePath(tb.dir)
	}
	if tb.browser {
		return pageHost(tb.url)
	}
	st := a.tabStatus(tb)
	s, p, ok := a.find(tb.path)
	where := filepath.Base(tb.dir)
	if tb.shell() && !ok {
		what := cmp.Or(tb.run, "shell")
		if st.State != status.Idle {
			what = string(st.State)
			if st.Msg != "" {
				what = st.Msg
			}
		}
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
	if used := usageText(s); ok && used != "" {
		where += " · " + used
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

func (a *App) tabKind(tb *tab) (string, *ui.SVG) {
	if tb.chooser {
		return "New pane", iconPlus
	}
	if tb.browser {
		return "Browser", iconGlobe
	}
	if tb.files != nil {
		return "Files", iconFolder
	}
	if tb.database != nil {
		return "Database", iconDatabase
	}
	if tb.shell() {
		return "Terminal", iconTerm
	}
	if a.inWorktree(tb.dir) {
		return "Worktree session", iconBranch
	}
	return "Larik session", iconChat
}

// tabMenu is what a tab's context menu offers.
func (a *App) tabMenu(m *ui.Menu, tb *tab) {
	if len(a.workspaces) > 1 {
		for _, w := range a.workspaces {
			if w != a.workspaceOf(tb) && m.Item("Move to "+w.name).Chosen() {
				_ = a.movePaneToWorkspace(tb, w)
			}
		}
		m.Separator()
	}
	if s, _, ok := a.find(tb.path); ok {
		a.answerMenu(m, s)
	}
	if p := a.paneOf(tb); p != nil && m.Item("Close Pane").Chosen() {
		a.closePane(p)
	}
	if tb.shell() && tb.hosted {
		if a.canDetach(tb) {
			if m.Item("Detach Pane").Chosen() {
				a.detachPane(tb)
			}
		} else if name, _, remote := splitRemote(tb.dir); remote {
			label := "Detach Pane — update Larik on " + name
			if tb.disconnected {
				label = "Detach Pane — reconnect to " + name
			}
			m.Item(label).Disabled(true)
		}
	}
	if tb.agentOffered && m.Item("Resume agent").Chosen() {
		a.resumeAgent(tb)
	}
	if tb.agentOffered && m.Item("Use Terminal").Chosen() {
		a.declineAgent(tb)
	}
	if tb.exited && tb.code != 0 && !tb.agentOffered && m.Item("Restart").Chosen() {
		a.restart(tb)
	}
	if tb.browser && tb.url != blankPage {
		a.pageMenu(m, tb)
	}
	if len(a.panes()) > 1 && m.Item("Close Other Panes").Chosen() {
		a.closeOthers(tb)
	}
	if (len(a.closed) > 0 || len(a.recent) > 0) && m.Item("Reopen Closed Pane").Chosen() {
		a.reopenClosed()
	}
	if len(a.recent) > 0 && m.Item("Recently Closed…").Chosen() {
		a.recentOpen, a.recentQuery = true, ""
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
			selected = tabKey(f)
			if b, ok := bookmarkForTab(f); ok && a.bookmarks.has(b) {
				selected = "bookmark:" + b.key()
			}
			if f.database != nil && f.database.profile != "" {
				selected = "database:" + f.database.profile
			}
		}
		searching := strings.TrimSpace(a.query) != ""
		var found []session.Indexed
		if searching {
			a.searchConversations()
			found = a.matches()
			if len(found) > 0 {
				selected = found[min(a.pick, len(found)-1)].Path // the match Enter opens
			}
		}
		a.rows = a.rows[:0] // sessionItem lists them again
		sb := ui.Sidebar(c, &selected, func() {
			if searching {
				a.results(c, t, found)
				return
			}
			a.pinnedSection(c, t)
			a.detachedSection(c, t)
			a.bookmarksSection(c, t)
			a.databaseSidebar(c, t)
			if len(a.servers) > 0 {
				machineHeading(c, t, "this-mac", iconLaptop, thisMachine())
			}
			for _, p := range a.projects {
				if !isRemote(p.Root) {
					a.project(c, t, p)
				}
			}
			for _, srv := range a.servers {
				a.serverSection(c, t, srv)
			}
		}).Grow(1).Background(ui.Transparent) // the rows on the sidebar's material, as macOS draws them
		// A click with Command or Shift chooses sessions together, and a
		// Shift+Up or Down takes in the next one; either opens nothing.
		toggle, extend := multiMods(c.Modifiers())
		if f := a.focused(); sb.Changed() && selected != "" && (f == nil || f.path != selected && tabKey(f) != selected) {
			switch {
			case strings.HasPrefix(selected, "database:"):
				a.openDatabaseProfile(strings.TrimPrefix(selected, "database:"))
			case extend && slices.Contains(a.rows, selected):
				a.pickRange(cmp.Or(a.anchor, a.cursor, a.focusedSession()), selected)
				a.cursor = selected
				a.announcePicked(c)
			case toggle:
			default:
				a.clearPicked()
				a.selectKey(selected)
			}
		}
		// While it has the keyboard: Shift+Up and Down take in the next
		// session, Escape lets go of those chosen, and Command+Delete
		// (Delete elsewhere) moves them to the Trash.
		switch {
		case sb.Shortcut(ui.Shift, ui.KeyDown):
			a.pickStep(1, selected)
			a.announcePicked(c)
		case sb.Shortcut(ui.Shift, ui.KeyUp):
			a.pickStep(-1, selected)
			a.announcePicked(c)
		case len(a.picked) > 0 && sb.Shortcut(0, ui.KeyEscape):
			a.clearPicked()
			c.Announce("Selection cleared")
		case len(a.picked) > 1 && (sb.Shortcut(deleteMods(), ui.KeyBackspace) || sb.Shortcut(0, ui.KeyDelete)):
			a.trashPicked()
		}
		a.serverField(c, t)
		if len(a.projects) == 0 {
			ui.Text(c, "No sessions yet. Run larik in a project, open a folder ("+keyLabel(false, "O")+"), or drop one here.").TextColor(t.TextMuted).Padding(12)
		}
		a.footer(c, t)
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

func (a *App) bookmarksSection(c *ui.Context, t *ui.Theme) {
	items := a.bookmarks.all()
	if len(items) == 0 {
		return
	}
	ui.SidebarSection(c.Key("browser-bookmarks"), "Browser Bookmarks", nil, func() {
		for _, b := range items {
			b := b
			ui.SidebarItem(c, "bookmark:"+b.key(), iconStar, b.label()).Tooltip(b.address()).
				ContextMenu(func(m *ui.Menu) {
					if m.Item("Remove Bookmark").Chosen() {
						a.setBookmark(b, false)
					}
				})
		}
	})
}

// pinnedSection lists the pinned sessions above the projects. A session
// dragged onto it is pinned, where it's dropped; while one is dragged
// and nothing is pinned, it shows to be dropped on.
func (a *App) pinnedSection(c *ui.Context, t *ui.Theme) {
	// Dragging, as the last frame found it: the session dragged is
	// further down.
	dragging := a.dragging
	a.dragging = false
	var pinned []session.Indexed
	pinned = append(pinned, a.pinnedSessions()...)
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
		a.archived(c, t, p)
		if !p.Missing {
			row := ui.ButtonBase(c.Key("new")).Gap(6).Padding(4, 12, 4, 14).Radius(6).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label("New session in " + p.Name).Children(func() {
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
	if m.Item("Save Project Layout…").Chosen() {
		a.askSaveLayout(p.Root)
	}
	var names []string
	for name := range a.layouts[p.Root] {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if m.Item("Open Layout: " + name).Chosen() {
			if err := a.openProjectLayout(p.Root, name); err != nil {
				a.notice = err.Error()
			}
		}
	}
	if len(names) > 0 {
		m.Separator()
	}
	if !p.Missing && m.Item("New Session Here").Chosen() {
		a.newTab(p.Root, "", "")
	}
	_, gitErr := os.Lstat(filepath.Join(p.Root, ".git"))
	if isRemote(p.Root) {
		gitErr = nil
	}
	if gitErr == nil && !p.Missing && m.Item("New Session in Worktree").Chosen() {
		a.newWorktreeSession(p.Root)
	}
	open := "Open Folder"
	if runtime.GOOS == "darwin" {
		open = "Open in Finder"
	}
	if !p.Missing && !isRemote(p.Root) && m.Item(open).Chosen() && a.openPath != nil {
		a.openPath(p.Root)
	}
	if m.Item("Copy Path").Chosen() && a.copyText != nil {
		a.copyText(p.Root)
	}
}

// footLink is a quiet link under a project's sessions, reporting a click.
func (a *App) footLink(c *ui.Context, t *ui.Theme, key, text string) bool {
	b := ui.ButtonBase(c.Key(key)).Padding(4, 12, 4, 34).Cursor(ui.CursorPointer).Label(text).Children(func() {
		ui.Text(c, text).FontSize(12).TextColor(t.TextMuted)
	})
	return b.Clicked()
}

// resultsShown is how many sessions a search shows.
const resultsShown = 50

// results lists what the search finds, across every project, each with
// its project.
func (a *App) results(c *ui.Context, t *ui.Theme, found []session.Indexed) {
	title := fmt.Sprintf("%d found", len(found))
	if len(found) == 0 {
		title = "Nothing found"
	}
	shown := found[:min(len(found), resultsShown)]
	wanted := make(map[string]bool, len(shown))
	for _, s := range shown {
		wanted[s.Path] = true
	}
	projects := make(map[string]*session.Project, len(shown))
	for i := range a.projects {
		p := &a.projects[i]
		for _, s := range p.Sessions {
			if wanted[s.Path] {
				projects[s.Path] = p
			}
		}
	}
	ui.SidebarSection(c, title, nil, func() {
		for _, s := range shown {
			p := projects[s.Path]
			if p == nil {
				continue
			}
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
	if used := usageText(s); used != "" {
		tip += "\n" + used
	}
	item := ui.SidebarItem(c, s.Path, icon, label).Tooltip(tip)
	a.rows = append(a.rows, s.Path)
	picked := len(a.picked) > 1 && slices.Contains(a.picked, s.Path)
	if picked {
		// Chosen with others: tinted, as Finder shows a selection, and
		// told to assistive technology as selected. Drawn over the row's
		// background, so that the tint stays while the pointer is on it.
		tint := t.Accent.Alpha(0.28)
		item.Selected(true).Background(ui.Transparent).TextColor(t.Text).
			Draw(func(p *ui.Painter, r ui.Rect) { p.Fill(r, tint, t.Radius) })
	}
	if item.Clicked() && a.pickClick(s.Path, c.Modifiers()) {
		a.announcePicked(c)
	}
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
		full := s.Running && contextMarker(c, t, s.Usage)
		if !a.marker(c, t, s.Status, a.unread(s)) && !full {
			ui.Text(c, age(time.Since(s.Modified))).FontSize(11).TextColor(t.TextMuted).SingleLine().Tooltip(s.Modified.Format("Mon 2 Jan 2006, 15:04"))
		}
	}).ContextMenu(func(m *ui.Menu) {
		if picked {
			a.pickedMenu(m)
			return
		}
		a.sessionMenu(m, s)
	})
	return item
}

// sessionDetail shows under a session's row what it is doing, while it
// works or waits for you; the branch of the worktree it runs in, when
// that isn't its project's; and the ports its programs listen on, each a
// link that opens it in a browser tab.
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
			if name, _, remote := splitRemote(s.Path); remote {
				url = fmt.Sprintf("%s:%d", name, port)
			}
			tip := "Open " + url + " in a browser pane (" + clickMod() + "-click: in your browser)"
			link := ui.ButtonBase(c.Key(url)).Cursor(ui.CursorPointer).Tooltip(tip).Label("Open " + url).Children(func() {
				ui.Text(c, fmt.Sprintf(":%d", port)).FontSize(11).TextColor(t.Accent)
			})
			if link.Clicked() {
				a.openPort(s.Path, port, c.Modifiers()&(ui.Super|ui.Ctrl) != 0)
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
		a.openInSplit(s.Path, false)
	}
	if m.Item("Open in Split Below").Chosen() {
		a.openInSplit(s.Path, true)
	}
	if isRemote(s.Path) {
		// Its transcript is on the server: what changes or shows it is
		// for larik there.
		m.Separator()
		if m.Item("Copy Session ID").Chosen() && a.copyText != nil {
			a.copyText(s.ID)
		}
		return
	}
	if m.Item("Fork into a New Pane").Chosen() {
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
	if a.hidden[s.Path] && m.Item("Show in Sidebar").Chosen() {
		delete(a.hidden, s.Path)
	}
	if m.Item("Archive").Chosen() {
		a.archive(s.Path)
	}
	if m.Item("Move to Trash…").Chosen() {
		a.trash(s.Path)
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

// reveal opens the session at path and its project's section.
func (a *App) reveal(path string) {
	if _, p, ok := a.find(path); ok {
		*a.open[p.Root] = true
	}
	a.selectSession(path)
}

// main is the panes, with the changes beside them, or a hint when no
// tab is open.
func (a *App) main(c *ui.Context, t *ui.Theme) {
	if a.layout == nil {
		a.card(c, t, func() {
			a.noticeBar(c, t)
			empty := ui.Column(c).Fill().Children(func() {
				dir := cmp.Or(a.emptyDir, a.currentDir())
				a.chooser(c, t, "No Session Open", "Pick a session in the sidebar, or open a new pane", &a.emptyPick, dir, func(d string) { a.emptyDir = d }, func(i int) {
					if tb := a.startKind(i, dir); tb != nil {
						a.activate(tb)
					}
				})
			})
			if d, ok := ui.Drop[sessionDrag](empty); ok {
				a.dropSession(string(d), nil)
			}
			if _, ok := ui.DragOver[sessionDrag](empty); ok {
				empty.Background(t.Accent.Alpha(0.08))
			}
		})
		return
	}
	// The changes are the focused tab's project's: they follow it.
	if a.changes != nil && a.changes.dir != a.focused().dir {
		a.closeChanges()
		a.toggleChanges()
	}
	panes := func() {
		ui.Column(c).Fill().Children(func() {
			a.noticeBar(c, t)
			root := a.layout
			if a.zoomed {
				root = a.focus
			}
			a.paneTree(c, t, root)
		})
	}
	if plan, tb := a.focusedPlan(); tb != nil {
		ui.SplitPercent(c, &a.planP, panes, func() { a.planPane(c, t, plan, tb) }).Grow(1)
	} else if a.changes != nil {
		ui.SplitPercent(c, &a.changesP, panes, func() { a.changesPane(c, t, a.changes) }).Grow(1)
	} else {
		ui.Column(c).Grow(1).Children(panes)
	}
}

// splitTip is the split button's tooltip.
func splitTip() string {
	return "New pane beside this one (" + keyLabel(false, "D") + ")"
}

// paneTree draws p: a pane, or a split's two, beside each other or one
// over the other, with the divider between them to drag.
func (a *App) paneTree(c *ui.Context, t *ui.Theme, p *pane) {
	if !p.split() {
		if p.tab != nil && a.paneOpacity < 1 && (p.tab.term != nil || p.tab.files != nil || p.tab.database != nil) {
			ui.Column(c).Fill().Background(t.Background.Alpha(a.paneOpacity)).Clip().Children(func() { a.pane(c, t, p) })
		} else {
			a.card(c, t, func() { a.pane(c, t, p) })
		}
		return
	}
	first, second := func() { a.paneTree(c, t, p.first) }, func() { a.paneTree(c, t, p.second) }
	if p.below {
		ui.SplitVerticalPercent(c, &p.percent, first, second).Grow(1)
	} else {
		ui.SplitPercent(c, &p.percent, first, second).Grow(1)
	}
}

// card is a pane: flush with the window's edges and its neighbour, as
// macOS's split views are.
func (a *App) card(c *ui.Context, t *ui.Theme, fn func()) {
	ui.Column(c).Fill().Background(t.Background).Clip().Children(fn)
}

func (a *App) noticeBar(c *ui.Context, t *ui.Theme) {
	if a.notice == "" && a.saveError == "" {
		return
	}
	if a.saveError != "" {
		ui.Row(c.Key("save-error")).Gap(8).Padding(8, 14).AlignItems(ui.Center).Background(t.Danger.Alpha(0.1)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, a.saveError).FontSize(12).Grow(1).Shrink(1)
		})
	}
	if a.notice != "" {
		ui.Row(c.Key("notice")).Gap(8).Padding(8, 14).AlignItems(ui.Center).Background(t.Warning.Alpha(0.1)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
			ui.Text(c, a.notice).FontSize(12).Grow(1).Shrink(1)
			a.closeButton(c, t, "Dismiss", func() { a.notice = "" })
		})
	}
}

// pane is a pane's tab: its header, find bar and terminal.
func (a *App) pane(c *ui.Context, t *ui.Theme, p *pane) {
	tb := p.tab
	var term ui.Element
	body := ui.Column(c).Grow(1).Children(func() {
		a.header(c, t, p)
		a.exitBar(c, t, tb)
		a.paneFeedback(c, t, tb)
		if tb.chooser {
			term = a.chooserPane(c, t, tb)
			return
		}
		if tb.files != nil {
			term = a.filesPane(c, t, tb)
			return
		}
		if tb.database != nil {
			term = a.databasePane(c, t, tb)
			return
		}
		if tb.browser {
			a.browserTabs(c, t, tb)
			a.addressBar(c, t, tb)
			a.siteBars(c, t, tb)
			if tb.finding {
				a.pageFindBar(c, t, tb)
			}
			if tb.web != nil {
				term = ui.WebView(c.Key(tb.id), tb.web).Grow(1)
			} else {
				term = ui.Box(c.Key(tb.id)).Grow(1)
			}
			return
		}
		if tb.finding {
			a.findBar(c, t, tb)
		}
		if tb.term != nil {
			term = terminal.View(c.Key(tb.id), tb.term).Grow(1)
		} else {
			term = ui.Box(c.Key(tb.id)).Grow(1)
		}
	})
	// A tab, a session or a pane dragged here goes beside this one, at
	// the edge it's near, or in its place.
	body.Label("Pane: " + a.tabTitle(tb))
	a.dropTarget(c, t, body, p)
	if a.needsLook(tb) {
		body.Border(1, a.ringColor(t, tb).Alpha(0.5))
	}
	// The keyboard anywhere in the pane, its find bar too, makes its tab
	// the focused one.
	if a.focusNext == tb {
		if tb.web != nil {
			tb.web.Focus()
		} else {
			term.Focus()
		}
		a.focusNext = nil
	} else if (body.FocusWithin() || tb.browser && term.Hovered()) && a.focus != p {
		a.focus = p
		if tb.files == nil {
			a.followFiles(tb.dir)
		}
	}
}

// exitBar says a tab's larik failed, over what it printed, and offers
// to run it again.
func (a *App) exitBar(c *ui.Context, t *ui.Theme, tb *tab) {
	if !tb.agentOffered && (!tb.exited || tb.code == 0) {
		return
	}
	if tb.disconnected {
		ui.Row(c.Key("disconnected")).Padding(6, 10, 6, 16).Background(t.Danger.Alpha(0.08)).Children(func() {
			ui.Text(c, "Remote connection lost. Reconnecting…").FontSize(12)
		})
		return
	}
	ui.Row(c.Key("exit")).Gap(8).Padding(6, 10, 6, 16).AlignItems(ui.Center).Background(t.Danger.Alpha(0.08)).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		ui.Icon(c, iconError).FontSize(13).TextColor(t.Danger)
		what := "larik exited with status " + strconv.Itoa(tb.code)
		if tb.agentOffered {
			what = "Resume the previous " + tb.agent.Kind + " session?"
		}
		if tb.path != "" {
			what += "; restarting resumes the session"
		}
		ui.Text(c, what).FontSize(12).Grow(1).Shrink(1)
		if tb.agentOffered {
			if ui.Button(c.Key("resume-agent"), "Resume agent").Clicked() {
				a.resumeAgent(tb)
			}
			if ui.Button(c.Key("decline-agent"), "Terminal").Clicked() {
				a.declineAgent(tb)
			}
		} else if ui.Button(c.Key("restart"), "Restart").Clicked() {
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

// header names the item shown in each pane, including the sole pane.
func (a *App) header(c *ui.Context, t *ui.Theme, p *pane) {
	tb := p.tab
	active := tb == a.focused()
	background, border := t.Text.Alpha(0.03), t.Border
	if active {
		background, border = t.Accent.Alpha(0.08), t.Accent.Alpha(0.55)
	}
	// Dragged by its header, the pane moves: to another pane's edge, or
	// in its place, swapping the two.
	head := ui.Row(c.Key("header")).Height(28).Gap(6).Padding(0, 6, 0, 12).AlignItems(ui.Center).Background(background).BorderWidth(0, 0, 2, 0).BorderColor(border).
		Cursor(ui.CursorGrab).Description("Drag to move this pane")
	head.Drag(paneDrag{p})
	head.ContextMenu(func(m *ui.Menu) { a.tabMenu(m, tb) })
	head.Children(func() {
		statusDot(c, t, a.tabStatus(tb))
		kind, kindIcon := a.tabKind(tb)
		ui.Icon(c, kindIcon).FontSize(12).TextColor(t.TextMuted).Tooltip(kind)
		if _, loading, _ := paneFeedbackState(tb); loading {
			ui.Spinner(c).Size(12, 12).Label("Loading")
		}
		title := ui.Text(c, a.tabTitle(tb)).FontSize(12).SingleLine().Shrink(1)
		if active {
			title.Bold()
		}
		ui.Text(c, a.where(tb)).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		newButton := ui.Box(c.Key("new-pane")).Padding(4).Radius(5).Label("New pane beside this one").
			Tooltip("Open a new pane beside this one").Children(func() {
			ui.Icon(c, iconPlus).FontSize(12).TextColor(t.TextMuted)
		})
		if newButton.Hovered() {
			newButton.Background(t.Text.Alpha(0.06))
		}
		newButton.Menu(func(m *ui.Menu) { a.creationMenu(m, p) })
		if a.split() {
			if a.zoomed {
				a.smallButton(c, t, "zoom", iconShrink, "Show every pane ("+keyText("Shift+Cmd+Enter")+")", a.toggleZoom)
			} else {
				a.smallButton(c, t, "zoom", iconZoom, "Zoom: this pane alone ("+keyText("Shift+Cmd+Enter")+")", func() {
					a.focusPane(p)
					a.toggleZoom()
				})
			}
		}
		if a.canDetach(tb) {
			a.smallButton(c, t, "detach", iconDetach, "Detach pane (keep terminal running)", func() { a.detachPane(tb) })
		}
		a.closeButton(c, t, "Close pane", func() { a.closePane(p) })
	})
}

func (a *App) iconButton(c *ui.Context, t *ui.Theme, key string, icon *ui.SVG, tip string, on bool, fn func()) ui.Element {
	// As macOS's toolbar buttons: borderless, a gray face while pressed
	// in or hovered.
	b := ui.Box(c.Key(key)).Size(32, 32).Center().Focusable().Radius(6).Label(tip).Tooltip(tip).Children(func() {
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
	a.smallButton(c, t, "close", iconClose, label, fn)
}

// smallButton is a borderless icon button, as a bar's close button.
func (a *App) smallButton(c *ui.Context, t *ui.Theme, key string, icon *ui.SVG, label string, fn func()) {
	b := ui.ButtonBase(c.Key(key)).Padding(4).Radius(5).Label(label).Tooltip(label).Children(func() {
		ui.Icon(c, icon).FontSize(12).TextColor(t.TextMuted)
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
