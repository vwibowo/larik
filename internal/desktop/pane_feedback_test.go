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
		{name: "browser loading", tab: &tab{browser: true, loading: true}, message: "Loading page…", loading: true},
		{name: "browser failure", tab: &tab{browser: true, pageError: "connection refused"}, message: "Page load failed: connection refused", failed: true},
		{name: "files search", tab: &tab{files: &filesState{query: "readme", search: fileSearch{loading: true}}}, message: "Searching files…", loading: true},
		{name: "files failure", tab: &tab{files: &filesState{root: "/repo", dirs: map[string]*fileDirectory{"/repo": {err: "permission denied"}}}}, message: "Can't open folder: permission denied", failed: true},
		{name: "database loading", tab: &tab{database: &databaseState{loading: true, status: "Loading schema…"}}, message: "Loading schema…", loading: true},
		{name: "database failure", tab: &tab{database: &databaseState{status: "connection refused"}}, message: "connection refused", failed: true},
		{name: "idle", tab: &tab{browser: true}, message: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			message, loading, failed := paneFeedbackState(tt.tab)
			if message != tt.message || loading != tt.loading || failed != tt.failed {
				t.Fatalf("got (%q, loading=%v, failed=%v)", message, loading, failed)
			}
		})
	}
}

func TestPaneFeedbackRendered(t *testing.T) {
	a := New(Options{Cwd: "/work"})
	tb := &tab{id: "browser", browser: true, pageError: "connection refused", dir: "/work"}
	a.tabs = []*tab{tb}
	a.activate(tb)
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	if !tt.HasText("Page load failed: connection refused") {
		t.Fatalf("pane feedback missing from %q", tt.Texts())
	}
}
