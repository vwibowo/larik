package desktop

import (
	"cmp"
	"fmt"
	"net"
	"net/url"
	"runtime"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// Browser tabs show a web page beside the sessions: a dev server, the
// docs, a pull request, in the OS's own web view (WKWebView on macOS),
// as Rex and cmux keep one. A browser tab is a pane like the others, so
// it splits, zooms and moves as they do. It lives in the window, not
// the host, and comes back from its page's address when the window
// opens again. Its pages share a session of their own, so what you sign
// in to stays signed in, and they are someone else's (Untrusted): they
// call none of the window's Go and get no permission.

// browserSession keeps the browser tabs' cookies and storage, apart
// from anything else's, from one run to the next.
var browserSession = mygo.NewSession("larik-browser")

// blankPage is what a new browser tab shows until you go somewhere.
const blankPage = "about:blank"

// webViewOptions are a browser tab's web view's, loading u.
func webViewOptions(u string) mygo.WebViewOptions {
	return mygo.WebViewOptions{URL: u, Page: mygo.PageOptions{
		Session:         browserSession,
		Untrusted:       true,
		SwipeNavigation: true,
		PinchZoom:       true,
		DevTools:        mygo.DevToolsEnabled,
	}}
}

// startBrowser opens a browser tab on u, which the address bar takes
// the keyboard for when it's blank.
func (a *App) startBrowser(u string) *tab {
	a.notice = ""
	if n, ok := normalizeURL(u); ok {
		u = n
	}
	u = cmp.Or(u, blankPage)
	t := &tab{id: newTabID(), dir: a.currentDir(), browser: true, url: u, addr: u}
	if u == blankPage {
		t.addr, t.focusAddr = "", true
	}
	if a.newWebView != nil {
		v, err := a.newWebView(u)
		if err != nil {
			a.notice = "Can't open a browser tab: " + err.Error()
			return nil
		}
		t.web = v
		a.watchPage(t)
		a.watchSite(t)
	}
	a.tabs = append(a.tabs, t)
	return t
}

// openBrowser opens a browser tab on u, blank for "", in the focused pane.
func (a *App) openBrowser(u string) {
	if t := a.startBrowser(u); t != nil {
		a.activate(t)
	}
}

// watchPage follows what t's page does: where it went, its title, and
// whether it's loading. A link opening a window opens a tab instead.
func (a *App) watchPage(t *tab) {
	p := t.web.Page()
	nav := func() {
		u, back, fwd := p.URL(), p.CanGoBack(), p.CanGoForward()
		a.update(func() {
			if u != "" {
				if t.addr == t.url || t.addr == "" && u != blankPage {
					t.addr = u
				}
				t.url = u
			}
			t.back, t.fwd = back, fwd
		})
	}
	t.off = append(t.off,
		p.OnWillNavigate(func(*mygo.NavigateEvent) {}), // an untrusted page goes nowhere without one
		p.OnDidNavigate(func(string) {
			a.update(func() { t.loading = true })
			nav()
		}),
		p.OnPageTitleUpdated(func(e *mygo.TitleEvent) {
			title := e.Title
			a.update(func() { t.title = title })
		}),
		p.OnDidFinishLoad(func() {
			a.update(func() { t.loading = false })
			nav()
		}),
		p.OnDidFailLoad(func(err *mygo.LoadError) {
			a.update(func() { t.loading = false })
			nav()
		}),
	)
	p.SetWindowOpenHandler(func(req mygo.WindowOpenRequest) *mygo.WindowOptions {
		if req.UserInitiated && req.URL != "" {
			u := req.URL
			a.update(func() { a.openBrowser(u) })
		}
		return nil
	})
}

// closeBrowser stops following t's page and closes it.
func (t *tab) closeBrowser() {
	for _, off := range t.off {
		off()
	}
	t.off = nil
	if t.web != nil {
		t.web.Destroy()
	}
}

// goTo loads what the address bar says in t.
func (a *App) goTo(t *tab) {
	u, ok := normalizeURL(t.addr)
	if !ok {
		a.notice = "“" + strings.TrimSpace(t.addr) + "” isn't a web address."
		return
	}
	a.notice = ""
	t.addr, t.url = u, u
	if t.web != nil {
		if err := t.web.Page().LoadURL(u); err != nil {
			a.notice = "Can't open " + u + ": " + err.Error()
			return
		}
		t.web.Focus()
	}
}

// normalizeURL makes what's typed in an address bar a URL: one with a
// scheme as it is, a host and maybe a path over https, or http for this
// machine's. Words that aren't an address are not one.
func normalizeURL(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return "", false
	}
	if u, err := url.Parse(s); err == nil {
		switch scheme := strings.ToLower(u.Scheme); {
		case scheme == "about" || scheme == "file":
			return s, true
		case (scheme == "http" || scheme == "https") && u.Host != "":
			return s, true
		}
	}
	host := s
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	switch {
	case host == "localhost" || strings.HasSuffix(host, ".localhost"):
		return "http://" + s, true
	case net.ParseIP(host) != nil:
		if ip := net.ParseIP(host); ip.IsLoopback() || ip.IsPrivate() {
			return "http://" + s, true
		}
		return "https://" + s, true
	case strings.Contains(host, ".") && !strings.HasPrefix(host, ".") && !strings.HasSuffix(host, "."):
		return "https://" + s, true
	}
	return "", false
}

// openPort shows what the session at path serves on port: in the
// browser tab already on it, or a new one beside the session's pane, or
// with external in your browser.
func (a *App) openPort(path string, port int, external bool) {
	u := fmt.Sprintf("http://localhost:%d", port)
	if external {
		a.openURL(u)
		return
	}
	for _, t := range a.tabs {
		if t.browser && pageHost(t.url) == pageHost(u) {
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

// pageHost is the host of a browser tab's page, for where it is.
func pageHost(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

// focusAddress gives the focused browser tab's address bar the
// keyboard, or opens a browser tab when no browser tab has it.
func (a *App) focusAddress() {
	if t := a.focused(); t != nil && t.browser {
		t.focusAddr = true
		return
	}
	a.openBrowser("")
}

// browse runs fn on the focused tab's page, when it's a browser tab's.
func (a *App) browse(fn func(p *mygo.Page)) {
	if t := a.focused(); t != nil && t.browser && t.web != nil {
		fn(t.web.Page())
	}
}

// addressBar is a browser tab's back, forward and reload buttons, its
// address, and a button opening the page in your browser.
func (a *App) addressBar(c *ui.Context, t *ui.Theme, tb *tab) {
	ui.Row(c.Key("address")).Gap(4).Padding(5, 10, 5, 8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
		page := func(fn func(p *mygo.Page)) func() {
			return func() {
				if tb.web != nil {
					fn(tb.web.Page())
				}
			}
		}
		a.navButton(c, t, "back", iconBack, "Back ("+keyText("Cmd+[")+")", tb.back, page((*mygo.Page).GoBack))
		a.navButton(c, t, "forward", iconForward, "Forward ("+keyText("Cmd+]")+")", tb.fwd, page((*mygo.Page).GoForward))
		if tb.loading {
			a.navButton(c, t, "stop", iconClose, "Stop loading", true, page((*mygo.Page).Stop))
		} else {
			a.navButton(c, t, "reload", iconReload, "Reload ("+keyText("Cmd+R")+")", true, page((*mygo.Page).Reload))
		}
		field := ui.TextInput(c.Key("field"), &tb.addr).Placeholder("Enter a web address").Label("Address").Grow(1).
			OnSubmit(func() { a.goTo(tb) }).
			OnShortcut(0, ui.KeyEscape, func() {
				tb.addr = tb.url
				if tb.url == blankPage {
					tb.addr = ""
				}
				if tb.web != nil {
					tb.web.Focus()
				}
			})
		if tb.focusAddr {
			field.Focus()
			tb.focusAddr = false
		}
		if tb.loading {
			ui.Spinner(c).Size(12, 12)
		}
		a.navButton(c, t, "external", iconExternal, "Open in your browser", tb.url != "" && tb.url != blankPage, func() { a.openURL(tb.url) })
	})
}

// browsers lists the browser tabs above the projects, under the
// terminals: each page's title, and its host or a spinner while it
// loads.
func (a *App) browsers(c *ui.Context, t *ui.Theme) {
	var pages []*tab
	for _, tb := range a.tabs {
		if tb.browser {
			pages = append(pages, tb)
		}
	}
	if len(pages) == 0 {
		return
	}
	ui.SidebarSection(c.Key("browsers"), "Browser", nil, func() {
		for _, tb := range pages {
			ui.SidebarItem(c, tabKey(tb), iconGlobe, a.tabTitle(tb)).Tooltip(tb.url).Children(func() {
				if tb.loading {
					ui.Spinner(c).Size(11, 11)
				} else if tb.url != blankPage {
					ui.Text(c, pageHost(tb.url)).FontSize(11).TextColor(t.TextMuted).SingleLine().MaxWidth(110)
				}
			}).ContextMenu(func(m *ui.Menu) { a.tabMenu(m, tb) })
		}
	})
}

// pageMenu is what a browser tab's menu does with its page.
func (a *App) pageMenu(m *ui.Menu, tb *tab) {
	if m.Item("Reload Page").Chosen() && tb.web != nil {
		tb.web.Page().Reload()
	}
	if m.Item("Copy Address").Chosen() && a.copyText != nil {
		a.copyText(tb.url)
	}
	if m.Item("Open in Your Browser").Chosen() {
		a.openURL(tb.url)
	}
}

// navButton is a borderless icon button, dimmed and doing nothing while
// it's off.
func (a *App) navButton(c *ui.Context, t *ui.Theme, key string, icon *ui.SVG, label string, on bool, fn func()) {
	color := t.TextMuted
	if !on {
		color = t.TextMuted.Alpha(0.35)
	}
	b := ui.Box(c.Key(key)).Padding(4).Radius(5).Label(label).Tooltip(label).Children(func() {
		ui.Icon(c, icon).FontSize(13).TextColor(color)
	})
	if !on {
		return
	}
	if b.Hovered() {
		b.Background(t.Text.Alpha(0.06))
	}
	if b.Clicked() {
		fn()
	}
}
