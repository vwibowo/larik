package app

import (
	"context"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"larik/internal/config"
	"larik/internal/llm"
	"larik/internal/providers"
)

type answerProvider struct{}

func (answerProvider) Name() string { return "local" }
func (answerProvider) Stream(_ context.Context, req llm.Request) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		msg := llm.Message{Role: llm.RoleAssistant, Model: req.Model, Blocks: []llm.Block{llm.TextBlock("TOP-SECRET-ANSWER")}}
		yield(llm.StreamEvent{Type: llm.EventDone, Message: msg, StopReason: llm.StopEnd, Usage: llm.Usage{Input: 12, Output: 3}}, nil)
	}
}

func telemetryEnv(t *testing.T, settings string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("LARIK_OTLP_ENDPOINT", "")
	t.Setenv("LARIK_OTLP_HEADERS", "")
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(root, "config", "larik", "config.json")
	os.MkdirAll(filepath.Dir(cfgPath), 0o700)
	if err := os.WriteFile(cfgPath, []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	return cwd
}

const localProvider = `"model":"local/test","providers":{"local":{"type":"openai-compatible","base_url":"http://127.0.0.1:1/v1"}}`

func TestTelemetryIsOffUnlessConfigured(t *testing.T) {
	cwd := telemetryEnv(t, `{`+localProvider+`}`)
	a, err := Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if a.Telemetry != nil || a.TelemetryNote != "" {
		t.Errorf("no endpoint, no export: %v %q", a.Telemetry, a.TelemetryNote)
	}
}

func TestBadTelemetryEndpointIsANoteNotAFailure(t *testing.T) {
	cwd := telemetryEnv(t, `{`+localProvider+`}`)
	t.Setenv("LARIK_OTLP_ENDPOINT", "otel.example:4318")
	a, err := Setup(cwd, "test")
	if err != nil {
		t.Fatalf("a broken telemetry setting must not stop Larik: %v", err)
	}
	defer a.Close()
	if a.Telemetry != nil || !strings.Contains(a.TelemetryNote, "http(s) URL") {
		t.Errorf("want a note about the endpoint, got %v %q", a.Telemetry, a.TelemetryNote)
	}
}

func TestSessionExportsToTheConfiguredCollector(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies, auth = append(bodies, string(b)), r.Header.Get("X-Token")
		mu.Unlock()
	}))
	defer srv.Close()

	cwd := telemetryEnv(t, `{`+localProvider+`,"telemetry":{"otlp_endpoint":"`+srv.URL+`"}}`)
	t.Setenv("LARIK_OTLP_HEADERS", "X-Token=from-env")
	a, err := Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.Resolve = func(_ *config.Config, spec string) (providers.Resolved, error) {
		return providers.Resolved{Provider: answerProvider{}, Model: "test"}, nil
	}
	s, err := a.Open(Options{})
	if err != nil {
		t.Fatal(err)
	}
	for range s.Agent.Run(context.Background(), "tell me the SECRET-PROMPT") {
	}
	s.Close("other")
	a.Close() // flushes

	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(bodies, "\n")
	for _, want := range []string{`"larik.turn"`, `"chat test"`, `"gen_ai.usage.input_tokens"`, s.ID, `"service.version"`} {
		if !strings.Contains(all, want) {
			t.Errorf("export lacks %s:\n%s", want, all)
		}
	}
	for _, secret := range []string{"SECRET-PROMPT", "TOP-SECRET-ANSWER"} {
		if strings.Contains(all, secret) {
			t.Errorf("the export carries content: %q", secret)
		}
	}
	if auth != "from-env" {
		t.Errorf("the header from LARIK_OTLP_HEADERS was not sent: %q", auth)
	}
}

func TestEnvironmentEndpointOverridesTheFile(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
	}))
	defer srv.Close()
	cwd := telemetryEnv(t, `{`+localProvider+`,"telemetry":{"otlp_endpoint":"http://127.0.0.1:1"}}`)
	t.Setenv("LARIK_OTLP_ENDPOINT", srv.URL)
	a, err := Setup(cwd, "test")
	if err != nil {
		t.Fatal(err)
	}
	a.Resolve = func(*config.Config, string) (providers.Resolved, error) {
		return providers.Resolved{Provider: answerProvider{}, Model: "test"}, nil
	}
	s, err := a.Open(Options{})
	if err != nil {
		t.Fatal(err)
	}
	for range s.Agent.Run(context.Background(), "hi") {
	}
	s.Close("other")
	a.Close()
	mu.Lock()
	defer mu.Unlock()
	if hits == 0 {
		t.Fatal("the endpoint from the environment should win over the file's")
	}
}
