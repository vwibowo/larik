package desktop

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPaneFeedbackState(t *testing.T) {
	for _, tt := range []struct {
		name    string
		tab     *tab
		message string
		loading bool
		failed  bool
	}{
		{name: "browser loading", tab: &tab{pane: &browserState{Loading: true}}, loading: true},
		{name: "browser failure", tab: &tab{pane: &browserState{PageError: "connection refused"}}, message: "Page load failed: connection refused", failed: true},
		{name: "files search", tab: &tab{pane: &filesState{Query: "readme", Search: fileSearch{Loading: true}}}, loading: true},
		{name: "files root", tab: &tab{pane: &filesState{Root: "/repo", Dirs: map[string]*fileDirectory{"/repo": {Loading: true}}}}, loading: true},
		{name: "files failure", tab: &tab{pane: &filesState{Root: "/repo", Dirs: map[string]*fileDirectory{"/repo": {Err: "permission denied"}}}}, message: "Can't open folder: permission denied", failed: true},
		{name: "database loading", tab: &tab{pane: &databaseState{Loading: true, Status: "Loading schema…"}}, loading: true},
		{name: "database export loading", tab: &tab{pane: &databaseState{Status: "Loading rows for CSV export…"}}, loading: true},
		{name: "database failure", tab: &tab{pane: &databaseState{Status: "connection refused"}}, message: "connection refused", failed: true},
		{name: "idle", tab: &tab{pane: &browserState{}}, message: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			message, loading, failed := paneFeedbackState(tt.tab)
			if message != tt.message || loading != tt.loading || failed != tt.failed {
				t.Fatalf("got (%q, loading=%v, failed=%v)", message, loading, failed)
			}
		})
	}
}

func TestPaneLoadingShownInTitle(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	tb := &tab{id: "browser-loading", pane: &browserState{Loading: true, URL: "https://example.com/docs"}, dir: "/work"}
	a.tabs = []*tab{tb}
	a.activate(tb)
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if _, ok := tt.Find("Loading"); !ok {
		t.Fatalf("pane title has no loading spinner: %q", tt.Texts())
	}
	if tt.HasText("Loading page") || tt.HasText("example.com — loading") {
		t.Fatalf("loading status still occupies a separate header strip: %q", tt.Texts())
	}
	if !tt.HasText("example.com") {
		t.Fatalf("browser host was removed from the pane header: %q", tt.Texts())
	}
}

func TestFilesLoadingKeepsContentPlaceholder(t *testing.T) {
	a := New(Options{Cwd: "/repo"})
	tb := &tab{
		id:   "files-loading",
		dir:  "/repo",
		pane: &filesState{Root: "/repo", Dirs: map[string]*fileDirectory{"/repo": {Loading: true}}},
	}
	a.tabs = []*tab{tb}
	a.activate(tb)
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if _, ok := tt.Find("Loading"); !ok || !tt.HasText("Loading…") {
		t.Fatalf("title spinner or contextual tree placeholder missing: %q", tt.Texts())
	}
	if tt.HasText("Loading files…") {
		t.Fatalf("shared loading label still rendered: %q", tt.Texts())
	}
}

func TestPaneFeedbackRendered(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	tb := &tab{id: "browser", pane: &browserState{PageError: "connection refused"}, dir: "/work"}
	a.tabs = []*tab{tb}
	a.activate(tb)
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if !tt.HasText("Page load failed: connection refused") {
		t.Fatalf("pane feedback missing from %q", tt.Texts())
	}
}
