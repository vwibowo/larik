package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
	session := &tab{id: "s", dir: "/work"}
	a.tabs = []*tab{session}
	a.activate(session)

	a.openBrowser("")
	b := a.focused()
	if b == nil || !b.browser || b.url != blankPage || b.addr != "" || !b.focusAddr || len(a.tabs) != 2 {
		t.Fatalf("a new browser tab: %+v", b)
	}
	if kind, _ := a.tabKind(b); kind != "Browser" || a.tabTitle(b) != "New browser tab" {
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
