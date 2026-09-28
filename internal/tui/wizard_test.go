package tui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/chatgpt"
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

func TestNVIDIAProviderWizardChoice(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w, _ := newWizard(cfg, true)
	for _, name := range []string{"nvidia-nim"} {
		w.provList.selectWhere(func(it pickItem) bool {
			c, ok := it.value.(providers.Choice)
			return ok && c.Name == name
		})
		it, _ := w.provList.selected()
		choice, ok := it.value.(providers.Choice)
		if !ok || choice.Name != name {
			t.Fatalf("setup wizard has no choice for %s: %+v", name, it)
		}
	}
	w.startAt("nvidia-nim")
	w.endpoint = providers.EndpointOf("nvidia-nim", config.ProviderConfig{APIKey: "key"})
	w.enterModels([]providers.Model{{ID: "meta/llama-test", Chat: true}})
	w.chooseModel("meta/llama-test")
	if w.step != wizSave {
		t.Fatalf("NVIDIA model rejected: step=%d err=%q", w.step, w.err)
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

// fakeOllama serves an empty model list until a pull succeeds.
func fakeOllama(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	installed := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/tags":
			var ms []map[string]any
			for _, m := range installed {
				ms = append(ms, map[string]any{"name": m, "capabilities": []string{"completion", "tools"}})
			}
			json.NewEncoder(w).Encode(map[string]any{"models": ms})
		case "/api/pull":
			var body struct{ Model string }
			json.NewDecoder(r.Body).Decode(&body)
			w.Write([]byte(`{"status":"pulling 3e4c","total":10,"completed":10}` + "\n" + `{"status":"success"}` + "\n"))
			installed = append(installed, body.Model)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// run feeds cmd's messages back into the wizard until it goes quiet.
func run(w *wizard, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		if msg == nil {
			return
		}
		cmd = w.update(msg)
	}
}

func TestWizardDownloadsAModel(t *testing.T) {
	srv := fakeOllama(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := config.Load(t.TempDir())
	w, _ := newWizard(cfg, true)
	w.startAt("ollama")
	w.fields[0].SetValue(srv.URL + "/v1")
	run(w, w.update(press(tea.KeyEnter)))
	if w.step != wizModel {
		t.Fatalf("step %d err %q", w.step, w.err)
	}
	it, _ := w.models.selected()
	if d, ok := it.value.(pickDownload); !ok || d.id != providers.OllamaSuggestions[0].ID {
		t.Fatalf("with nothing installed the first download should be selected, got %+v", it)
	}
	run(w, w.update(press(tea.KeyEnter)))
	if w.step != wizSave || w.model != providers.OllamaSuggestions[0].ID || w.pulling != "" {
		t.Fatalf("after the download the wizard should move on to saving it: step %d model %q err %q", w.step, w.model, w.err)
	}

	// A typed id that isn't installed is offered as a download.
	w.step = wizModel
	w.models.filter = "phi4:14b"
	vis := w.models.visible()
	if last := vis[len(vis)-1]; last.value != (pickDownload{"phi4:14b"}) {
		t.Fatalf("typed id should be downloadable: %+v", last)
	}
}

func TestWizardStopsADownload(t *testing.T) {
	w := &wizard{step: wizModel, endpoint: providers.Endpoint{Kind: "ollama", BaseURL: "http://127.0.0.1:1"}}
	w.pulling, w.pullStop = "qwen3:8b", func() {}
	w.gen = 3
	w.update(wizPullMsg{gen: 3, done: true, err: context.Canceled})
	if w.pulling != "" || !strings.Contains(w.err, "download stopped") {
		t.Fatalf("pulling %q err %q", w.pulling, w.err)
	}
}

func TestModelPickerOffersDownloads(t *testing.T) {
	m := testModel(t)
	m.openModelPicker()
	m.Update(modelsLoadedMsg{map[string]providerModels{"ollama": {models: []providers.Model{{ID: "m", Chat: true}}}}})
	var row pickItem
	for _, it := range m.mpick.list.items {
		if strings.Contains(it.label, "Download a model") {
			row = it
		}
	}
	if row.value != (pickConnect{"ollama"}) {
		t.Fatalf("the Ollama section should offer downloads through the wizard: %+v", m.mpick.list.items)
	}
}

func TestWizardSignsInToChatGPT(t *testing.T) {
	// A fake OAuth server and a fake Codex backend with no model list.
	idToken := "e30." + base64.RawURLEncoding.EncodeToString([]byte(`{"email":"me@example.com","https://api.openai.com/auth":{"chatgpt_account_id":"acct_1"}}`)) + ".s"
	access := "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))) + ".s"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			json.NewEncoder(w).Encode(map[string]string{"id_token": idToken, "access_token": access, "refresh_token": "rt"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	oldIssuer, oldOpen := chatgpt.Issuer, openBrowser
	chatgpt.Issuer = srv.URL
	openBrowser = func(signIn string) { // the user signs in
		u, _ := url.Parse(signIn)
		go http.Get(u.Query().Get("redirect_uri") + "?code=c&state=" + url.QueryEscape(u.Query().Get("state")))
	}
	defer func() { chatgpt.Issuer, openBrowser = oldIssuer, oldOpen }()

	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, _ := config.Load(t.TempDir())
	cfg.Providers["codex"] = config.ProviderConfig{BaseURL: srv.URL}
	w, _ := newWizard(cfg, false)
	w.startAt("codex")
	if w.useSaved || !strings.Contains(plain(strings.Join(w.signInView(newStyles(true)), "\n")), "Sign in with the ChatGPT account") {
		t.Fatal("not signed in yet: the step should offer to sign in")
	}
	run(w, w.update(press(tea.KeyEnter)))
	if w.step != wizModel || w.err != "" {
		t.Fatalf("after signing in the wizard should list models: step %d err %q", w.step, w.err)
	}
	if !chatgpt.SignedIn(chatgpt.Path(cfg.ConfigDir)) || !strings.Contains(w.savedKey, "me@example.com") {
		t.Fatalf("sign-in should be saved: %q", w.savedKey)
	}
	w.models.selectWhere(func(it pickItem) bool { return it.value == "gpt-6-luna" })
	if it, _ := w.models.selected(); it.value != "gpt-6-luna" {
		t.Fatal("the cheapest model should be offered")
	}
}
