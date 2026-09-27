package tui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/config"
	"larik/internal/providers"
)

func press(k rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: k} }

func typed(s string) tea.KeyPressMsg {
	r := []rune(s)
	return tea.KeyPressMsg{Code: r[0], Text: s}
}

func TestPickerFilterAndSkipDisabled(t *testing.T) {
	p := picker{filterable: true, items: []pickItem{
		{label: "alpha", value: 1},
		{label: "beta", disabled: true},
		{label: "gamma", value: 3},
		{label: "+ Add provider…", keep: true, value: 4},
	}}
	p.home()
	p.handleKey(press(tea.KeyDown))
	if it, _ := p.selected(); it.value != 3 {
		t.Fatalf("down should skip the disabled row, got %+v", it)
	}
	p.handleKey(typed("gam"))
	if vis := p.visible(); len(vis) != 2 || vis[0].label != "gamma" {
		t.Fatalf("filter should keep gamma and the pinned row: %+v", vis)
	}
	if !p.handleKey(press(tea.KeyEnter)) {
		t.Fatal("enter should choose")
	}
	p.handleKey(press(tea.KeyBackspace))
	if p.filter != "ga" {
		t.Fatalf("backspace: filter %q", p.filter)
	}
}

func TestPickerScrollKeepsCursorVisible(t *testing.T) {
	p := picker{height: 4}
	for _, l := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		p.items = append(p.items, pickItem{label: l, value: l})
	}
	p.home()
	for range 5 {
		p.handleKey(press(tea.KeyDown))
	}
	out := p.view(newStyles(true), 40)
	if !strings.Contains(out, "f") || strings.Count(out, "\n") != 3 {
		t.Fatalf("cursor row f should be in a 4-line window:\n%s", out)
	}
}

func TestWizardConnectsOllama(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"models":[
			{"name":"embeddinggemma:latest","capabilities":["embedding"]},
			{"name":"qwen3:4b","capabilities":["completion","tools","thinking"]}]}`))
	}))
	defer srv.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	w, _ := newWizard(cfg, true)
	w.update(wizDetectedMsg{map[string]providers.Detection{"ollama": {Name: "ollama", Running: true, Models: 2}}})
	if it, _ := w.provList.selected(); it.label != "Ollama" {
		t.Fatalf("a detected provider should be preselected, got %q", it.label)
	}
	w.update(press(tea.KeyEnter))
	if w.step != wizConnect {
		t.Fatalf("step %d, want connect", w.step)
	}
	w.fields[0].SetValue(srv.URL + "/v1")
	cmd := w.update(press(tea.KeyEnter))
	if cmd == nil || !w.busy {
		t.Fatal("enter should start a connection test")
	}
	w.update(cmd())
	if w.step != wizModel {
		t.Fatalf("step %d, want model (err %q)", w.step, w.err)
	}
	if it, _ := w.models.selected(); it.value != "qwen3:4b" {
		t.Fatalf("the tool-capable model should be preselected, got %+v", it)
	}
	w.update(press(tea.KeyEnter))
	if w.step != wizSave || !w.makeDefault {
		t.Fatalf("step %d default %v", w.step, w.makeDefault)
	}
	w.update(press(tea.KeyEnter))
	if !w.done || w.result.Spec() != "ollama/qwen3:4b" {
		t.Fatalf("done %v result %+v err %q", w.done, w.result, w.err)
	}
	data, err := os.ReadFile(cfg.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"model": "ollama/qwen3:4b"`) || !strings.Contains(string(data), srv.URL) {
		t.Fatalf("saved config:\n%s", data)
	}
}

func TestWizardRejectsBadInput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	cfg, _ := config.Load(t.TempDir())
	w, _ := newWizard(cfg, false)
	w.startAt("openai")
	if w.step != wizConnect || w.useSaved {
		t.Fatalf("step %d useSaved %v", w.step, w.useSaved)
	}
	if cmd := w.update(press(tea.KeyEnter)); cmd != nil || !strings.Contains(w.err, "paste a key") {
		t.Fatalf("an empty key must not be tested: err %q", w.err)
	}
	w.update(press(tea.KeyEscape))
	if w.step != wizProvider || w.err != "" {
		t.Fatalf("esc should go back and clear the error: step %d err %q", w.step, w.err)
	}
	w.update(press(tea.KeyEscape))
	if !w.canceled {
		t.Fatal("esc on the first step cancels")
	}
}

func TestWizardSaveScopeFollowsSelectionNotCursor(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := config.Load(t.TempDir())
	w := &wizard{cfg: cfg, step: wizSave, choice: providers.Choice{Name: "ollama", Title: "Ollama"}, model: "qwen3:4b", makeDefault: true}
	w.update(press(tea.KeyDown))
	w.update(press(tea.KeyDown)) // passes the project row on the way to the toggle
	if w.scope != 0 {
		t.Fatalf("moving the cursor must not change where settings are saved (scope %d)", w.scope)
	}
	w.update(press(tea.KeySpace))
	if w.makeDefault {
		t.Fatal("space on the toggle should turn it off")
	}
	w.update(press(tea.KeyUp))
	w.update(press(tea.KeySpace))
	if w.scope != 1 {
		t.Fatal("space on the project row should select it")
	}
}
