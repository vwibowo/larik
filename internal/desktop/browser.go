package desktop

import (
	"cmp"
	"fmt"
	"runtime"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	browserpane "larik/internal/desktop/panes/browser"
)

// Browser tabs show a web page beside the sessions: a dev server, the
// docs, a pull request, in the OS's own web view (WKWebView on macOS),
// as Rex and cmux keep one. A browser tab is a pane like the others, so
// it splits, zooms and moves as they do. It lives in the window, not
// the host, and comes back from its page's address when the window
// opens again. Its pages share a session of their own, so what you sign
// in to stays signed in, and they are someone else's (Untrusted): they
// call none of the window's Go and get no permission.

// blankPage is what a new browser tab shows until you go somewhere.
const blankPage = browserpane.BlankPage

// webViewOptions are a browser tab's web view's, loading u.
func webViewOptions(u string) mygo.WebViewOptions {
	return browserpane.WebViewOptions(u)
}

type browserState = browserpane.State

func (t *tab) browserState() *browserpane.State {
	if t == nil {
		return nil
	}
	b, _ := t.pane.(*browserpane.State)
	return b
}
func (t *tab) isBrowser() bool { return t.browserState() != nil }

// startBrowser opens a browser tab on u, which the address bar takes
// the keyboard for when it's blank.
func (a *App) startBrowser(u string) *tab {
	a.notice = ""
	if n, ok := normalizeURL(u); ok {
		u = n
	}
	u = cmp.Or(u, blankPage)
	t := &tab{id: newTabID(), dir: a.currentDir(), pane: &browserpane.State{URL: u, Address: u}}
	if u == blankPage {
		t.browserState().Address, t.browserState().FocusAddress = "", true
	}
	if a.newWebView != nil {
		v, err := a.newWebView(u)
		if err != nil {
			a.notice = "Can't open a browser pane: " + err.Error()
			return nil
		}
		t.browserState().Web = v
		a.watchPage(t)
		a.watchSite(t)
	}
	a.tabs = append(a.tabs, t)
	return t
}

// openBrowser opens a browser tab on u, blank for "", in a pane.
func (a *App) openBrowser(u string) {
	if t := a.startBrowser(u); t != nil {
		a.activate(t)
	}
}

// newBrowserTab adds a page to the focused browser pane, retaining its pane
// while keeping the inactive page open in the window.
func (a *App) newBrowserTab() {
	p := a.focus
	t := a.startBrowser("")
	if t == nil {
		return
	}
	if p != nil && p.tab != nil && p.tab.isBrowser() {
		p.tab = t
		a.focusPane(p)
		return
	}
	a.activate(t)
}

// cycleBrowserTab selects the browser page before or after the focused one.
func (a *App) cycleBrowserTab(by int) {
	current := a.focused()
	if current == nil || !current.isBrowser() {
		return
	}
	var pages []*tab
	for _, page := range a.tabs {
		if page.isBrowser() {
			pages = append(pages, page)
		}
	}
	if len(pages) < 2 {
		return
	}
	i := 0
	for j, page := range pages {
		if page == current {
			i = j
			break
		}
	}
	if p := a.paneOf(current); p != nil {
		p.tab = pages[(i+by+len(pages))%len(pages)]
		a.focusPane(p)
	}
}

// browserTabs renders the browser pages as a tab strip inside the pane.
func (a *App) browserTabs(c *ui.Context, t *ui.Theme, current *tab) {
	firstLast := "Ctrl+Shift+1"
	if runtime.GOOS == "darwin" {
		firstLast = "Cmd+1"
	}
	firstLastEnd := "Ctrl+Shift+9"
	if runtime.GOOS == "darwin" {
		firstLastEnd = "Cmd+9"
	}
	pageCount := 0
	for _, page := range a.tabs {
		if page.isBrowser() {
			pageCount++
		}
	}
	index := 0
	ui.Row(c.Key("browser-tabs")).Gap(3).Padding(4, 8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		for _, page := range a.tabs {
			if !page.isBrowser() {
				continue
			}
			page := page
			pageIndex := index
			index++
			label := a.tabTitle(page)
			tooltip := label
			if pageIndex == 0 {
				tooltip += " · First tab (" + keyText(firstLast) + ")"
			}
			if pageIndex == pageCount-1 {
				tooltip += " · Last tab (" + keyText(firstLastEnd) + ")"
			}
			ui.Row(c.Key("browser-tab-"+page.id)).Gap(2).AlignItems(ui.Center).Padding(2, 4).Radius(5).Children(func() {
				b := ui.Box(c.Key("browser-tab-select-"+page.id)).Padding(3, 6).Label("Browser tab: " + label).Tooltip(tooltip).Children(func() { ui.Text(c, label).FontSize(12).Bold().TextColor(t.TextMuted) })
				if page == current {
					b.Background(t.Text.Alpha(0.08))
				} else if b.Hovered() {
					b.Background(t.Text.Alpha(0.06))
				}
				if b.Clicked() && page != current {
					if p := a.paneOf(page); p != nil {
						a.focusPane(p)
					} else if p := a.paneOf(current); p != nil {
						p.tab = page
						a.focusPane(p)
					} else {
						a.activate(page)
					}
				}
				a.navButton(c, t, "browser-tab-close-"+page.id, iconClose, "Close browser tab", true, func() { a.askClose([]*tab{page}, nil) })
			})
		}
		shortcutHelp := "New Browser Tab (" + keyText("Cmd+T") + ") · Next: Ctrl+Tab · Previous: Ctrl+Shift+Tab · First: " + keyText(firstLast) + " · Last: " + keyText(firstLastEnd)
		a.navButton(c, t, "new-browser-tab", iconPlus, shortcutHelp, true, a.newBrowserTab)
	})
}

// watchPage follows what t's page does: where it went, its title, and
// whether it's loading. A link opening a window opens a tab instead.
func (a *App) watchPage(t *tab) {
	p := t.browserState().Web.Page()
	nav := func() {
		u, back, fwd := p.URL(), p.CanGoBack(), p.CanGoForward()
		a.update(func() {
			t.browserState().ApplyNavigation(u, back, fwd)
			if u != "" && t.remotePort.port != 0 && u != blankPage {
				t.remoteBookmarkSuffix = bookmarkSuffix(u)
			}
		})
	}
	t.browserState().Observers = append(t.browserState().Observers,
		p.OnWillNavigate(func(*mygo.NavigateEvent) {}), // an untrusted page goes nowhere without one
		p.OnDidNavigate(func(string) {
			a.update(func() { t.browserState().Loading, t.browserState().PageError = true, "" })
			nav()
		}),
		p.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
			title := e.Title
			a.update(func() { t.title = title })
		}),
		p.OnDidFinishLoad(func() {
			a.update(func() { t.browserState().Loading = false })
			nav()
		}),
		p.OnDidFailLoad(func(err *mygo.LoadError) {
			message := err.Description
			if message == "" {
				message = err.Error()
			}
			a.update(func() { t.browserState().Loading, t.browserState().PageError = false, message })
			nav()
		}),
	)
	p.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		a.openBrowserWindow(t, req)
		return nil
	})
}

// openBrowserWindow routes a user-opened page into a browser tab in the
// originating pane. Script-created popups are ignored.
func (a *App) openBrowserWindow(from *tab, req mygo.WindowOpenRequest) {
	if !req.UserInitiated || req.URL == "" {
		return
	}
	a.update(func() {
		if page := a.startBrowser(req.URL); page != nil {
			if pane := a.paneOf(from); pane != nil {
				pane.tab = page
				a.focusPane(pane)
			} else {
				a.activate(page)
			}
		}
	})
}

// goTo loads what the address bar says in t.
func (a *App) goTo(t *tab) {
	a.notice = ""
	u, search, err := t.browserState().GoToAddress()
	if err != nil {
		if search {
			a.notice = "Can't search Google: " + err.Error()
		} else {
			a.notice = "Can't open " + u + ": " + err.Error()
		}
	}
}

// searchWeb searches Google for query, or opens its home page when empty.
func (a *App) searchWeb(t *tab, query string) {
	if t == nil || !t.isBrowser() {
		return
	}
	a.notice = ""
	if _, err := t.browserState().Search(query); err != nil {
		a.notice = "Can't search Google: " + err.Error()
	}
}

// openPort shows what the session at path serves on port: in the
// browser tab already on it, or a new one beside the session's pane, or
// with external in your browser.
func (a *App) openPort(path string, port int, external bool) {
	if isRemote(path) {
		a.openRemotePort(path, port, external)
		return
	}
	u := fmt.Sprintf("http://localhost:%d", port)
	if external {
		a.openURL(u)
		return
	}
	for _, t := range a.tabs {
		if t.isBrowser() && pageHost(t.browserState().URL) == pageHost(u) {
			a.activate(t)
			return
		}
	}
	t := a.startBrowser(u)
	if t == nil {
		return
	}
	if p := a.paneOf(a.tabOf(path)); p != nil {
		a.focusPane(p)
		a.splitPane(p, t, false)
		return
	}
	a.activate(t)
}

// clickMod names the key that makes a click open a link in your
// browser.
func clickMod() string {
	if runtime.GOOS == "darwin" {
		return "⌘"
	}
	return "Ctrl"
}

// focusAddress gives the focused browser tab's address bar the
// keyboard, or opens a browser tab when no browser tab has it.
func (a *App) focusAddress() {
	if t := a.focused(); t != nil && t.isBrowser() {
		t.browserState().FocusAddress = true
		return
	}
	a.openBrowser("")
}

// browse runs fn on the focused browser tab's page, when it's one.
func (a *App) browse(fn func(p *mygo.Page)) {
	if t := a.focused(); t != nil && t.isBrowser() && t.browserState().Web != nil {
		fn(t.browserState().Web.Page())
	}
}

// refreshFocused refreshes only the pane with the window's focus.
func (a *App) refreshFocused() {
	t := a.focused()
	if t == nil {
		return
	}
	switch {
	case t.isBrowser():
		if t.browserState().Web != nil {
			a.browse((*mygo.Page).Reload)
		}
	case t.filesState() != nil:
		a.refreshFiles(t)
	case t.databaseState() != nil:
		if !t.databaseState().Loading {
			a.loadDatabaseSchema(t)
		}
	}
}

// addressBar is a browser tab's back, forward and reload buttons, its
// address, and a button opening the page in your browser.
func (a *App) addressBar(c *ui.Context, theme *ui.Theme, tb *tab) {
	remoteLabel := ""
	if tb.remotePort.port != 0 {
		remoteLabel = fmt.Sprintf("%s:%d", tb.remotePort.name, tb.remotePort.port)
		if tb.remotePortLost {
			remoteLabel += " disconnected"
		}
	}
	bookmark, canBookmark := bookmarkForTab(tb)
	browserpane.AddressBar(c, theme, browserpane.AddressBarOptions{
		State: tb.browserState(), RemoteLabel: remoteLabel,
		BackIcon: iconBack, ForwardIcon: iconForward, StopIcon: iconClose, ReloadIcon: iconReload,
		ExternalIcon: iconExternal, StarIcon: iconStar, StarFilledIcon: iconStarFilled,
		BackShortcut: keyText("Cmd+["), ForwardShortcut: keyText("Cmd+]"), ReloadShortcut: keyText("Cmd+R"),
		GoTo: func() { a.goTo(tb) }, OpenExternal: a.openURL, ToggleBookmark: func() { a.toggleBookmark(tb) },
		BookmarkAvailable: canBookmark, Bookmarked: canBookmark && a.bookmarks.has(bookmark),
	})
}

// pageMenu is what a browser tab's menu does with its page.
func (a *App) pageMenu(m *ui.Menu, tb *tab) {
	if b, ok := bookmarkForTab(tb); ok {
		label := "Bookmark Page"
		if a.bookmarks.has(b) {
			label = "Remove Bookmark"
		}
		if m.Item(label).Chosen() {
			a.toggleBookmark(tb)
		}
	}
	if m.Item("Refresh Pane").Chosen() && tb.browserState().Web != nil {
		tb.browserState().Web.Page().Reload()
	}
	if m.Item("Copy Address").Chosen() && a.copyText != nil {
		a.copyText(tb.browserState().URL)
	}
	if m.Item("Open in Your Browser").Chosen() {
		a.openURL(tb.browserState().URL)
	}
}

// findPage finds what t's find bar says in its page, from the first
// match, or with next the one after (before, with back), off the main
// thread, as the page answers.
func (a *App) findPage(t *tab, next, back bool) {
	text := t.find
	if t.browserState().Web == nil {
		return
	}
	if text == "" {
		a.endPageFind(t)
		return
	}
	p := t.browserState().Web.Page()
	go func() {
		res, err := p.FindInPage(text, mygo.FindOptions{FindNext: next, Backward: back})
		a.update(func() {
			if err == nil && t.find == text {
				t.browserState().Found = res
			}
		})
	}()
}

// endPageFind takes the find bar's highlights off t's page.
func (a *App) endPageFind(t *tab) {
	t.browserState().Found = mygo.FindResult{}
	if t.browserState().Web != nil {
		t.browserState().Web.Page().StopFindInPage()
	}
}

// pageFindBar finds text in a browser tab's page, as findBar does in a
// terminal: Enter goes to the next match, Shift+Enter back, and Escape
// clears the text, then closes the bar.
func (a *App) pageFindBar(c *ui.Context, t *ui.Theme, tb *tab) {
	ui.Row(c.Key("find")).Gap(6).Padding(6, 10, 6, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		field := ui.SearchField(c.Key("field"), &tb.find).Placeholder("Find").Label("Find in the page").Grow(1).MaxWidth(320).
			OnChange(func() { a.findPage(tb, false, false) }).
			OnSubmit(func() { a.findPage(tb, true, false) }).
			OnShortcut(ui.Shift, ui.KeyEnter, func() { a.findPage(tb, true, true) }).
			OnShortcut(0, ui.KeyEscape, func() { a.closeFind(tb) })
		if tb.focusFind {
			field.Focus()
			tb.focusFind = false
		}
		var count string
		switch {
		case tb.find == "":
		case tb.browserState().Found.Matches == 0:
			count = "No matches"
		default:
			count = fmt.Sprintf("%d of %d", tb.browserState().Found.Active, tb.browserState().Found.Matches)
		}
		ui.Text(c, count).FontSize(12).TextColor(t.TextMuted).SingleLine()
		ui.Spacer(c)
		a.iconButton(c, t, "previous", iconUp, "Previous match (Shift+Enter)", false, func() { a.findPage(tb, true, true) })
		a.iconButton(c, t, "next", iconDown, "Next match (Enter)", false, func() { a.findPage(tb, true, false) })
		a.closeButton(c, t, "Close find", func() { a.closeFind(tb) })
	})
}

// navButton is a borderless icon button, dimmed and doing nothing while
// it's off.
func (a *App) navButton(c *ui.Context, theme *ui.Theme, key string, icon *ui.SVG, label string, on bool, fn func()) {
	browserpane.NavButton(c, theme, key, icon, label, on, fn)
}

func normalizeURL(s string) (string, bool) { return browserpane.NormalizeURL(s) }
func pageHost(u string) string             { return browserpane.PageHost(u) }
