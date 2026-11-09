package desktop

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestBrowserBookmarkButtonAndPersistence(t *testing.T) {
	data := t.TempDir()
	a := New(Options{DataDir: data, Cwd: "/work"})
	a.openBrowser("https://example.com/docs")
	tb := a.focused()
	tb.title = "Example Docs"
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if _, ok := tt.Find("Bookmark this page"); !ok {
		t.Fatal("browser pane has no bookmark button")
	}
	if err := tt.Click("Bookmark this page"); err != nil {
		t.Fatal(err)
	}
	if len(a.bookmarks.all()) != 1 || !tt.HasText("Browser Bookmarks") || !tt.HasText("Example Docs") {
		t.Fatalf("bookmark or sidebar missing: %+v %q", a.bookmarks.all(), tt.Texts())
	}
	if _, ok := tt.Find("Remove bookmark"); !ok {
		t.Fatal("bookmarked page has no selected button")
	}
	if err := tt.Click("Remove bookmark"); err != nil || len(a.bookmarks.all()) != 0 {
		t.Fatalf("star did not remove bookmark: %v", err)
	}
	if err := tt.Click("Bookmark this page"); err != nil || len(a.bookmarks.all()) != 1 {
		t.Fatalf("star did not restore bookmark: %v", err)
	}
	// Repeated saves do not add a second row.
	b, _ := bookmarkForTab(tb)
	a.setBookmark(b, true)
	if len(a.bookmarks.all()) != 1 {
		t.Fatal("duplicate bookmark")
	}
	a.closeTab(tb)
	if !tt.HasText("Browser Bookmarks") {
		t.Fatal("closing the pane removed its bookmark")
	}
	if err := tt.Click("Example Docs"); err != nil || len(a.tabs) != 1 || a.focused().url != b.URL {
		t.Fatalf("sidebar bookmark did not open its page: %v", err)
	}
	other := New(Options{DataDir: data, Cwd: "/work"})
	if got := other.bookmarks.all(); len(got) != 1 || got[0].URL != "https://example.com/docs" || got[0].Title != "Example Docs" {
		t.Fatalf("bookmark did not persist: %+v", got)
	}
	other.openSavedBookmark(other.bookmarks.all()[0])
	if len(other.tabs) != 1 || other.focused().url != b.URL {
		t.Fatal("saved bookmark did not open a browser pane")
	}
	other.openSavedBookmark(other.bookmarks.all()[0])
	if len(other.tabs) != 1 {
		t.Fatal("opening a bookmark duplicated its open pane")
	}
	if err := tt.RightClick("Example Docs"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Remove Bookmark"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.bookmarks.all()) != 0 || len(loadBookmarkStore(data).all()) != 0 {
		t.Fatal("bookmark removal did not persist")
	}
}

func TestBlankBrowserPageCannotBeBookmarked(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	a.openBrowser("")
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if _, ok := tt.Find("Bookmark this page"); !ok {
		t.Fatal("blank page has no disabled bookmark button")
	}
	if err := tt.Click("Bookmark this page"); err != nil {
		t.Fatal(err)
	}
	if len(a.bookmarks.all()) != 0 {
		t.Fatal("blank page was bookmarked")
	}
}

func TestBookmarksSharedAcrossWindows(t *testing.T) {
	a := New(Options{DataDir: t.TempDir(), Cwd: "/work"})
	g := newWindowGroup(a)
	b := New(a.opts)
	b.bookmarks = a.bookmarks
	b.bookmarksChanged = g.refreshWindows
	b.openBookmark = func(bookmark browserBookmark) { g.openBookmark(b, bookmark) }
	g.children["second"] = &windowEntry{app: b}
	g.ids = []string{"second"}
	page := browserBookmark{URL: "https://example.com", Title: "Example"}
	a.setBookmark(page, true)
	if len(b.bookmarks.all()) != 1 {
		t.Fatal("second window did not see saved bookmark")
	}
	b.openBrowser(page.URL)
	a.openSavedBookmark(page)
	if len(a.tabs) != 0 || len(b.tabs) != 1 || g.active != b || b.focused() != b.tabs[0] {
		t.Fatal("bookmark did not focus the existing pane in another window")
	}
}

func TestRemoteBookmarkStoresDestinationAndPath(t *testing.T) {
	a := New(Options{DataDir: t.TempDir(), Cwd: "/work"})
	tb := &tab{browser: true, url: "http://127.0.0.1:49152/docs/a%20b?q=one#intro", remotePort: remotePortKey{"devbox", 3000}}
	b, ok := bookmarkForTab(tb)
	if !ok || b.URL != "" || b.RemoteName != "devbox" || b.RemotePort != 3000 || b.RemotePath != "/docs/a%20b?q=one#intro" {
		t.Fatalf("remote bookmark: %+v", b)
	}
	a.setBookmark(b, true)
	data, err := os.ReadFile(filepath.Join(a.opts.DataDir, "desktop", "bookmarks.json"))
	if err != nil || string(data) == "" || loadBookmarkStore(a.opts.DataDir).all()[0].RemotePath != b.RemotePath {
		t.Fatalf("remote bookmark not saved: %v %q", err, data)
	}
	// Reopening while disconnected reports the problem and leaves the bookmark.
	a.openSavedBookmark(b)
	if a.notice != "Not connected to devbox" || len(a.tabs) != 0 || len(a.bookmarks.all()) != 1 {
		t.Fatalf("disconnected remote bookmark: %q, %d tabs", a.notice, len(a.tabs))
	}
	// Once a forward is available, opening uses its new port and saved path.
	key := remotePortKey{"devbox", 3000}
	a.servers = []*server{{name: "devbox", features: []string{"remote-meta"}, dial: func() (net.Conn, error) { return nil, nil }}}
	a.forwards = map[remotePortKey]*remoteForward{key: {local: 5173}}
	a.openSavedBookmark(b)
	if len(a.tabs) != 1 {
		t.Fatalf("remote bookmark opened %d panes", len(a.tabs))
	}
	ready := a.tabs[0]
	if ready.url != "http://127.0.0.1:5173/docs/a%20b?q=one#intro" || ready.remotePortLost {
		t.Fatalf("remote bookmark reopened at %q", ready.url)
	}
	a.openSavedBookmark(b)
	if len(a.tabs) != 1 {
		t.Fatal("reopening a remote bookmark duplicated its pane")
	}
	if got := a.state().RemotePaths[0]; got != b.RemotePath {
		t.Fatalf("reopened remote page path not retained: %q", got)
	}
}
