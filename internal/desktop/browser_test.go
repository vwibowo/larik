package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/vwibowo/larik": "https://github.com/vwibowo/larik",
		"http://example.com":               "http://example.com",
		"github.com":                       "https://github.com",
		"  docs.example.com/a?b#c ":        "https://docs.example.com/a?b#c",
		"localhost:3000":                   "http://localhost:3000",
		"localhost:3000/app":               "http://localhost:3000/app",
		"localhost":                        "http://localhost",
		"app.localhost:8080":               "http://app.localhost:8080",
		"127.0.0.1:5173":                   "http://127.0.0.1:5173",
		"192.168.1.20":                     "http://192.168.1.20",
		"[::1]:8080":                       "http://[::1]:8080",
		"8.8.8.8":                          "https://8.8.8.8",
		"about:blank":                      "about:blank",
		"file:///tmp/index.html":           "file:///tmp/index.html",
		"":                                 "",
		"how do i":                         "",
		"larik":                            "",
		"mailto:me@example.com":            "",
		"javascript:alert(1)":              "",
		"http://":                          "",
	} {
		got, ok := normalizeURL(in)
		if got != want || ok != (want != "") {
			t.Errorf("normalizeURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestBrowserTab(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	session := &tab{id: "s", dir: "/work", chooser: true}
	a.tabs = []*tab{session}
	a.activate(session)

	a.openBrowser("")
	b := a.focused()
	if b == nil || !b.browser || b.url != blankPage || b.addr != "" || !b.focusAddr || len(a.tabs) != 2 {
		t.Fatalf("a new browser tab: %+v", b)
	}
	if kind, _ := a.tabKind(b); kind != "Browser" || a.tabTitle(b) != "New browser pane" {
		t.Fatalf("kind %q, title %q", kind, a.tabTitle(b))
	}
	// Typed into its address bar, a host goes over https.
	b.addr = "example.com/docs"
	a.goTo(b)
	if b.url != "https://example.com/docs" || a.tabTitle(b) != "example.com" || a.where(b) != "example.com" {
		t.Fatalf("after going: url %q, title %q, where %q", b.url, a.tabTitle(b), a.where(b))
	}
	b.addr = "not an address"
	a.goTo(b)
	if b.url != "https://example.com/docs" || a.notice == "" {
		t.Fatalf("words go nowhere: url %q, notice %q", b.url, a.notice)
	}
	// It splits beside a session as any tab does.
	a.activate(session)
	a.showInSplit(b, false)
	if shownAt(a, 0) != session || shownAt(a, 1) != b {
		t.Fatal("the browser tab beside the session")
	}
	// Cmd+L gives its address bar the keyboard, or opens one.
	b.focusAddr = false
	a.focusAddress()
	if !b.focusAddr || len(a.tabs) != 2 {
		t.Fatal("Open Location in a browser tab")
	}
	a.activate(session)
	a.focusAddress()
	if len(a.tabs) != 3 || !a.focused().browser {
		t.Fatal("Open Location elsewhere opens a browser tab")
	}

	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	for _, label := range []string{"Address", "Back (" + keyText("Cmd+[") + ")", "Reload (" + keyText("Cmd+R") + ")"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("no %q in %q", label, tt.Texts())
		}
	}
}

func TestBrowserTabsComeBack(t *testing.T) {
	data := t.TempDir()
	a := New(Options{DataDir: data, Cwd: "/work"})
	a.restore()
	a.openBrowser("localhost:3000")
	a.openBrowser("https://example.com")
	a.showInSplit(a.tabs[0], true)
	st := a.state()
	if len(st.Tabs) != 2 || st.Browsers[0] != "http://localhost:3000" || st.Browsers[1] != "https://example.com" {
		t.Fatalf("saved: %+v", st)
	}
	saved, _ := json.Marshal(st)
	if err := os.MkdirAll(filepath.Dir(a.statePath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.statePath(), saved, 0o600); err != nil {
		t.Fatal(err)
	}

	b := New(Options{DataDir: data, Cwd: "/work"})
	if !b.restore() || len(b.tabs) != 2 || !b.split() {
		t.Fatalf("restored %d tabs, split %v: %s", len(b.tabs), b.split(), b.notice)
	}
	if !b.tabs[0].browser || b.tabs[0].url != "http://localhost:3000" || b.tabs[1].url != "https://example.com" || b.focusNext == nil {
		t.Fatalf("restored: %+v %+v", b.tabs[0], b.tabs[1])
	}
}

func TestControlBrowserTab(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	res := a.command(Request{Cmd: "open", URL: "localhost:5173"})
	if res.Error != "" || res.Tab == nil || res.Tab.Kind != "browser" || res.Tab.URL != "http://localhost:5173" {
		t.Fatalf("open --url: %+v", res)
	}
	if res := a.command(Request{Cmd: "open", URL: "two words"}); res.Error == "" {
		t.Fatal("open --url with words")
	}
	for _, cmd := range []string{"send", "read"} {
		if res := a.command(Request{Cmd: cmd, Tab: res.Tab.ID, Text: "hi"}); res.Error != "a browser tab has no terminal" {
			t.Fatalf("%s to a browser tab: %+v", cmd, res)
		}
	}
	if res := a.command(Request{Cmd: "close", Tab: res.Tab.ID}); res.Error != "" || len(a.tabs) != 0 {
		t.Fatalf("close: %+v, %d tabs", res, len(a.tabs))
	}
}

func TestPortOpensBesideItsSession(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	s, other := &tab{id: "s", dir: "/work", path: "/s.jsonl"}, &tab{id: "o", dir: "/other"}
	a.tabs = []*tab{s, other}
	a.activate(other)
	a.showInSplit(s, false)
	a.activate(other)
	a.openPort("/s.jsonl", 3000, false)
	b := a.focused()
	if b == nil || !b.browser || len(a.panes()) != 3 || a.paneOf(b).parent != a.paneOf(s).parent {
		t.Fatalf("the port's tab beside its session: %d panes", len(a.panes()))
	}
}

func TestSidebarListsBrowserTabs(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	a.openBrowser("example.com")
	a.tabs[0].title = "Example Domain"
	a.openBrowser("localhost:3000")
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	for _, want := range []string{"2 panes", "Pane: Example Domain", "example.com", "localhost:3000"} {
		if !tt.HasText(want) {
			t.Fatalf("no %q in %q", want, tt.Texts())
		}
	}
	a.activate(a.tabs[0])
	if a.focused() != a.tabs[0] || len(a.panes()) != 2 {
		t.Fatal("focusing the browser keeps both panes visible")
	}
	var copied string
	a.copyText = func(s string) { copied = s }
	if err := tt.RightClick("Example Domain"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Copy Address"); err != nil {
		t.Fatalf("%v in %q", err, tt.Menu())
	}
	tt.Frame()
	if copied != "https://example.com" {
		t.Fatalf("copied %q", copied)
	}
}

func TestSitePermissions(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	a.openBrowser("meet.example.com")
	b := a.focused()
	call := mygo.PermissionRequest{Origin: "https://meet.example.com", Permissions: []mygo.Permission{mygo.PermissionNotifications}}
	// Asked first, it gets nothing yet, and the bar asks you.
	if a.permitted(b, call) || b.asking == nil || permissionText(b.asking.perms) != "your notifications" {
		t.Fatalf("first ask: %+v", b.asking)
	}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if !tt.HasText("meet.example.com wants to use your notifications.") {
		t.Fatalf("no bar in %q", tt.Texts())
	}
	if err := tt.Click("Allow"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if b.asking != nil || !a.permitted(b, call) {
		t.Fatal("allowed, it gets it")
	}
	// Don't Allow keeps it from asking again; another site still asks.
	other := mygo.PermissionRequest{Origin: "https://ads.example.com", Permissions: []mygo.Permission{mygo.PermissionNotifications}}
	a.permitted(b, other)
	a.answerSite(b, false)
	if a.permitted(b, other) || b.asking != nil {
		t.Fatal("denied, it doesn't ask again")
	}
	// The answers are kept.
	if st := a.state(); !st.Sites[siteKey(call.Origin, mygo.PermissionNotifications)] || st.Sites[siteKey(other.Origin, mygo.PermissionNotifications)] {
		t.Fatalf("saved: %v", st.Sites)
	}
	if got := permissionText([]mygo.Permission{mygo.PermissionCamera, mygo.PermissionMicrophone}); got != "your camera and microphone" {
		t.Fatal(got)
	}
}

func TestDownloadBar(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	var shown string
	a.showFile = func(p string) { shown = p }
	a.openBrowser("example.com")
	b := a.focused()
	b.download = &pageDownload{name: "report.pdf"}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if !tt.HasText("Downloading report.pdf…") {
		t.Fatalf("no progress in %q", tt.Texts())
	}
	a.downloaded(b, "/Users/me/Downloads/report (2).pdf", "")
	tt.Frame()
	if !tt.HasText("Downloaded report (2).pdf") || len(a.events) != 1 {
		t.Fatalf("done: %q, %d events", tt.Texts(), len(a.events))
	}
	if err := tt.Click(showInFolder()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if shown != "/Users/me/Downloads/report (2).pdf" {
		t.Fatalf("showed %q", shown)
	}
}

func TestFindInPageBar(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	a.openBrowser("example.com")
	b := a.focused()
	a.openFind()
	if !b.finding || !b.focusFind {
		t.Fatal("Cmd+F opens a browser tab's find bar")
	}
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if _, ok := tt.Find("Find in the page"); !ok {
		t.Fatalf("no find bar in %q", tt.Texts())
	}
	b.find, b.found = "domain", mygo.FindResult{Matches: 3, Active: 2}
	tt.Frame()
	if !tt.HasText("2 of 3") {
		t.Fatalf("no count in %q", tt.Texts())
	}
	a.closeFind(b)
	if b.finding || b.find != "" || b.found.Matches != 0 || a.focusNext != b {
		t.Fatal("closing the bar clears it")
	}
}

func TestPageCommandsFindTheirTab(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	if res := a.command(Request{Cmd: "list"}); res.Error != "" {
		t.Fatal(res.Error)
	}
	if _, msg := a.pageTab(Request{Op: PageSnapshot}); !strings.Contains(msg, "no browser tab is open") {
		t.Fatalf("with none: %q", msg)
	}
	s := &tab{id: "s", dir: "/work", path: "/s.jsonl"}
	a.tabs = []*tab{s}
	a.activate(s)
	// Going somewhere with none opens one beside the focused pane, which
	// keeps the keyboard.
	_, msg := a.pageTab(Request{Op: PageNavigate, URL: "localhost:3000"})
	if len(a.tabs) != 2 || !a.tabs[1].browser || a.tabs[1].url != "http://localhost:3000" || !a.split() || a.focused() != s {
		t.Fatalf("navigate with none: %d tabs, split %v, msg %q", len(a.tabs), a.split(), msg)
	}
	if msg != "the browser tab has no page" { // no window in a test
		t.Fatalf("msg %q", msg)
	}
	// Then it goes to the browser tab focused last; a named one must be a
	// browser tab.
	b := a.tabs[1]
	a.activate(b)
	a.activate(s)
	if got, _ := a.pageTab(Request{Op: PageSnapshot}); got != nil || a.lastBrowser != b {
		t.Fatalf("the one focused last: %v", a.lastBrowser)
	}
	if _, msg := a.pageTab(Request{Op: PageSnapshot, Tab: "s"}); msg != "the tab isn't a browser tab" {
		t.Fatalf("named: %q", msg)
	}
}
