package desktop

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"larik/internal/session"
)

// pathOf is the session titled title, archived or not.
func pathOf(t *testing.T, a *App, title string) string {
	t.Helper()
	for _, p := range a.projects {
		for _, s := range append(p.Sessions, p.Archived...) {
			if s.Title == title {
				return s.Path
			}
		}
	}
	t.Fatalf("no session %q", title)
	return ""
}

func TestArchiveAndTrash(t *testing.T) {
	data, _ := fixture(t)
	a, mu := serialApp(Options{DataDir: data})
	defer a.Close()
	const landing = "Polish the landing copy"
	mu.Lock()
	a.setProjects(index(t, data))
	a.restore()
	tt := ui.NewTester(a.view, 1240, 800)
	tt.Frame()
	path := pathOf(t, a, landing)
	session.SetName(path, "") // as it was

	// Archived from its menu, it leaves the project's list for its
	// archived ones, shown when asked for.
	if err := tt.RightClick(landing); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Archive"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if tt.HasText(landing) || !tt.HasText("Show 1 archived") || a.notice != "" {
		t.Fatalf("archived: %q, %s", tt.Texts(), a.notice)
	}
	archived := pathOf(t, a, landing)
	if !session.IsArchived(archived) {
		t.Fatalf("not moved: %s", archived)
	}
	if _, err := os.Stat(archived); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Show 1 archived"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText(landing) || !tt.HasText("Leave archived ones out") {
		t.Fatalf("archived ones: %q", tt.Texts())
	}
	snapshot(t, tt, "archived.png")
	// So the index reads it.
	a.setProjects(index(t, data))
	tt.Frame()
	if !tt.HasText(landing) || pathOf(t, a, landing) != archived {
		t.Fatalf("after a new index: %q", tt.Texts())
	}
	if err := tt.RightClick(landing); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Unarchive"); err != nil {
		t.Fatal(err, tt.Menu())
	}
	tt.Frame()
	if pathOf(t, a, landing) != path || tt.HasText("Leave archived ones out") {
		t.Fatalf("unarchived: %q", tt.Texts())
	}

	// A session a process has open stays.
	blocked := pathOf(t, a, "Add OSC 7501 output")
	if a.archive(blocked) || !strings.Contains(a.notice, "another terminal") {
		t.Fatalf("archived a running session: %s", a.notice)
	}
	a.notice = ""

	// To the Trash, after asking, in a folder of its own; when the Trash
	// won't take it, it stays where it was.
	var trashed []string
	refuse := true
	a.trashItem = func(p string) error {
		if refuse {
			return errors.New("no Trash on that drive")
		}
		trashed = append(trashed, p)
		return os.RemoveAll(p)
	}
	a.trash(path)
	if a.asked == nil || !strings.Contains(a.asked.title, "Move “"+landing+"” to the Trash?") {
		t.Fatalf("asks: %+v", a.asked)
	}
	a.asked.then()
	a.asked = nil
	mu.Unlock()
	waitFor(t, mu, "the refusal", func() bool { return strings.Contains(a.notice, "it's where it was") })
	if _, err := os.Stat(path); err != nil {
		t.Fatal("not put back:", err)
	}
	mu.Lock()
	a.setProjects(index(t, data))
	a.notice, refuse = "", false
	a.trashNow(path)
	mu.Unlock()
	waitFor(t, mu, "the Trash", func() bool { return len(trashed) == 1 })
	if base := filepath.Base(trashed[0]); !strings.HasPrefix(base, "larik session ") {
		t.Fatalf("trashed %s", trashed[0])
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("still there")
	}

	// The commands: archive, unarchive and trash, by id.
	mu.Lock()
	a.setProjects(index(t, data))
	var id string
	for _, p := range a.projects {
		for _, s := range p.Sessions {
			if s.Title == "Refactor the parser and run the tests" {
				id = s.ID
			}
		}
	}
	mu.Unlock()
	for _, cmd := range []string{"archive", "unarchive", "archive", "trash"} {
		if res := a.control(Request{Cmd: cmd, Session: id[:len(id)-2]}); res.Error != "" {
			t.Fatalf("%s: %s", cmd, res.Error)
		}
	}
	if res := a.control(Request{Cmd: "archive", Session: "nope"}); res.Error == "" {
		t.Fatal("archived no session")
	}
	if len(trashed) != 2 {
		t.Fatalf("trashed %v", trashed)
	}
}

// Names given in the window, kept in its state by earlier versions, go
// beside the transcripts.
func TestNamesMoveBesideTranscripts(t *testing.T) {
	data, _ := fixture(t)
	projects := index(t, data)
	var path string
	for _, p := range projects {
		for _, s := range p.Sessions {
			if s.Title == "Polish the landing copy" {
				path = s.Path
			}
		}
	}
	old, _ := json.Marshal(map[string]any{"tabs": []string{}, "names": map[string]string{path: "Landing"}})
	os.MkdirAll(filepath.Join(data, "desktop"), 0o700)
	os.WriteFile(filepath.Join(data, "desktop", "state.json"), old, 0o600)
	a, _ := serialApp(Options{DataDir: data})
	defer a.Close()
	a.setProjects(projects)
	a.restore()
	if got, _ := os.ReadFile(session.NamePath(path)); string(got) != "Landing\n" {
		t.Fatalf("name file: %q", got)
	}
	if s, _, _ := a.find(path); s.Title != "Landing" {
		t.Fatalf("titled %q", s.Title)
	}
	if st := a.state(); st.Names != nil {
		t.Fatalf("saved again in the state: %v", st.Names)
	}
}
