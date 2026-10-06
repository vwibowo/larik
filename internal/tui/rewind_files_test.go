package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/app"
	"larik/internal/checkpoint"
	"larik/internal/llm"
	"larik/internal/session"
)

// rewindFixture resumes a real session with three prompts. Prompt 2
// edited a.txt and created b.txt; prompt 3 edited a.txt again.
func rewindFixture(t *testing.T) (*model, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config", "larik", "config.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{"model":"local/test","providers":{"local":{"type":"openai-compatible","base_url":"http://127.0.0.1:1/v1"}}}`
	if err := os.WriteFile(configPath, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)

	s, err := session.Create(a.SessionDir, session.Meta{Cwd: cwd, Provider: "local", Model: "local/test"})
	if err != nil {
		t.Fatal(err)
	}
	store := checkpoint.New(filepath.Join(a.Cfg.DataDir, "checkpoints", s.ID), cwd)
	aFile, bFile := filepath.Join(cwd, "a.txt"), filepath.Join(cwd, "b.txt")
	if err := os.WriteFile(aFile, []byte("a0"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := func(text string) {
		time.Sleep(5 * time.Millisecond)
		store.BeginTurn()
		if err := s.AppendMessage(llm.UserText(text), nil); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	reply := func() {
		if err := s.AppendMessage(llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{llm.TextBlock("ok")}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	prompt("one")
	reply()
	prompt("two")
	store.Capture(aFile)
	os.WriteFile(aFile, []byte("a2"), 0o644)
	store.Capture(bFile)
	os.WriteFile(bFile, []byte("b2"), 0o644)
	reply()
	// Saved as the agent saves a prompt sent in plan mode, with a note
	// in front that /rewind and the prompt menu must not show.
	prompt("<system-note>\nPlan mode is on.\n</system-note>\n\nthree")
	store.Capture(aFile)
	os.WriteFile(aFile, []byte("a3"), 0o644)
	reply()
	id := s.ID
	s.Close()

	sess, err := a.Open(app.Options{ResumeID: id})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(Options{App: a, Config: a.Cfg, Agent: sess.Agent, Session: sess})
	t.Cleanup(func() { m.sess.Close("other") })
	return m, cwd
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestRewindOffersToRestoreFiles(t *testing.T) {
	m, cwd := rewindFixture(t)
	original := m.sess.ID
	m.command("/rewind 2")
	if m.rewindOffer == nil {
		t.Fatal("files changed after prompt 2, so /rewind 2 should ask")
	}
	if m.sess.ID != original {
		t.Fatal("nothing may branch before the question is answered")
	}
	if view := plain(m.View().Content); !strings.Contains(view, "Restore files too?") || !strings.Contains(view, "a.txt") || !strings.Contains(view, "b.txt") {
		t.Errorf("the offer should name the files:\n%s", view)
	}
	if fileText(t, filepath.Join(cwd, "a.txt")) != "a3" {
		t.Fatal("asking must not touch files")
	}

	m.Update(tea.KeyPressMsg{Code: '1', Text: "1"})
	if m.rewindOffer != nil || m.sess.ID == original {
		t.Fatal("choosing 1 should branch")
	}
	if got := fileText(t, filepath.Join(cwd, "a.txt")); got != "a0" {
		t.Errorf("a.txt = %q, want its state before prompt 2 (a0)", got)
	}
	if got := fileText(t, filepath.Join(cwd, "b.txt")); got != "<missing>" {
		t.Errorf("b.txt was created by prompt 2 and should be gone, got %q", got)
	}
	if m.input.Value() != "two" {
		t.Errorf("the rewound prompt goes back in the input, got %q", m.input.Value())
	}
}

func TestRewindCanKeepOrCancel(t *testing.T) {
	m, cwd := rewindFixture(t)
	original := m.sess.ID
	m.command("/rewind 2")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.rewindOffer != nil || m.sess.ID != original || fileText(t, filepath.Join(cwd, "a.txt")) != "a3" {
		t.Fatal("escape cancels the rewind and changes nothing")
	}

	m.command("/rewind 2")
	m.Update(tea.KeyPressMsg{Code: '2', Text: "2"})
	if m.sess.ID == original || fileText(t, filepath.Join(cwd, "a.txt")) != "a3" {
		t.Fatal("'Branch only' branches and leaves the files")
	}
}

func TestRewindArgumentsSkipTheQuestion(t *testing.T) {
	m, cwd := rewindFixture(t)
	m.command("/rewind 3 files")
	if m.rewindOffer != nil || fileText(t, filepath.Join(cwd, "a.txt")) != "a2" {
		t.Fatalf("/rewind 3 files restores only prompt 3's change (a2), got %q", fileText(t, filepath.Join(cwd, "a.txt")))
	}

	m2, cwd2 := rewindFixture(t)
	m2.command("/rewind 2 keep")
	if m2.rewindOffer != nil || fileText(t, filepath.Join(cwd2, "a.txt")) != "a3" {
		t.Fatal("/rewind 2 keep leaves files alone without asking")
	}

	// Prompt 1 changed no files, but later prompts did, so it still asks.
	m3, _ := rewindFixture(t)
	m3.command("/rewind 1")
	if m3.rewindOffer == nil {
		t.Error("rewinding to prompt 1 undoes later file changes too")
	}
	m3.rewindOffer = nil
	m3.command("/rewind 2 bogus")
	if m3.rewindOffer != nil {
		t.Error("an unknown option is an error, not a question")
	}
}
