package audio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAICompatibleAudioEndpoints(t *testing.T) {
	var gotSTT, gotTTS bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/audio/transcriptions":
			gotSTT = true
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("multipart: %v", err)
			}
			if r.FormValue("model") != "qwen-asr" {
				t.Errorf("model=%q", r.FormValue("model"))
			}
			if r.FormValue("language") != "id" {
				t.Errorf("language=%q", r.FormValue("language"))
			}
			w.Write([]byte(`{"text":" hello world ","language":"Indonesian"}`))
		case "/v1/audio/speech":
			gotTTS = true
			b, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(b), "qwen-tts") {
				t.Errorf("body=%s", b)
			}
			w.Header().Set("Content-Type", "audio/wav")
			w.Write([]byte("wav"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := New(Config{Enabled: true, STT: EndpointConfig{BaseURL: srv.URL + "/v1", Model: "qwen-asr", Language: "id"}, TTS: EndpointConfig{BaseURL: srv.URL + "/v1", Model: "qwen-tts", Voice: "default"}})
	text, err := s.Transcribe(context.Background(), []byte("audio"), "recording.wav")
	if err != nil || text != "hello world" {
		t.Fatalf("transcribe=%q err=%v", text, err)
	}
	data, mime, err := s.Synthesize(context.Background(), "say this")
	if err != nil || string(data) != "wav" || mime != "audio/wav" {
		t.Fatalf("speech=%q mime=%q err=%v", data, mime, err)
	}
	if !gotSTT || !gotTTS {
		t.Fatalf("STT=%v TTS=%v", gotSTT, gotTTS)
	}
}

func TestSTTCommandReadsTranscriptFromStdout(t *testing.T) {
	s := New(Config{Enabled: true, STT: EndpointConfig{Command: "cat"}})
	text, err := s.Transcribe(context.Background(), []byte("  run the tests\n"), "recording.wav")
	if err != nil || text != "run the tests" {
		t.Fatalf("transcribe=%q err=%v", text, err)
	}
}

func TestSTTCommandSeesCurrentLanguage(t *testing.T) {
	s := New(Config{Enabled: true, STT: EndpointConfig{Command: `printf '%s' "$LARIK_STT_LANGUAGE"; true`, Language: "id"}})
	if text, err := s.Transcribe(context.Background(), []byte("audio"), "recording.wav"); err != nil || text != "id" {
		t.Fatalf("transcribe=%q err=%v", text, err)
	}
	s.SetSTTLanguage("en")
	if text, err := s.Transcribe(context.Background(), []byte("audio"), "recording.wav"); err != nil || text != "en" {
		t.Fatalf("after SetSTTLanguage: transcribe=%q err=%v", text, err)
	}
}

func TestSTTCommandFailureCarriesStderr(t *testing.T) {
	s := New(Config{Enabled: true, STT: EndpointConfig{Command: "echo boom >&2; false"}})
	if _, err := s.Transcribe(context.Background(), []byte("audio"), "recording.wav"); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err=%v", err)
	}
	s = New(Config{Enabled: true, STT: EndpointConfig{Command: "true"}})
	if _, err := s.Transcribe(context.Background(), []byte("audio"), "recording.wav"); err == nil || !strings.Contains(err.Error(), "no text") {
		t.Fatalf("empty transcript err=%v", err)
	}
}

func TestCommandsTakePrecedenceOverEndpoints(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("endpoint called: %s", r.URL.Path)
	}))
	defer srv.Close()
	s := New(Config{Enabled: true,
		STT: EndpointConfig{Command: "cat", BaseURL: srv.URL, Model: "asr"},
		TTS: EndpointConfig{Command: "cat >", BaseURL: srv.URL, Model: "tts"},
	})
	if text, err := s.Transcribe(context.Background(), []byte("hello"), "recording.wav"); err != nil || text != "hello" {
		t.Fatalf("transcribe=%q err=%v", text, err)
	}
	data, mime, err := s.Synthesize(context.Background(), "say this")
	if err != nil || string(data) != "say this" || mime != "audio/wav" {
		t.Fatalf("speech=%q mime=%q err=%v", data, mime, err)
	}
}

func TestTTSCommandSeesVoice(t *testing.T) {
	s := New(Config{Enabled: true, TTS: EndpointConfig{Command: `printf '%s' "$LARIK_TTS_VOICE" >`, Voice: "Samantha"}})
	data, _, err := s.Synthesize(context.Background(), "ignored")
	if err != nil || string(data) != "Samantha" {
		t.Fatalf("speech=%q err=%v", data, err)
	}
}

func TestTTSFallsBackToDefaultCommand(t *testing.T) {
	saved := defaultSpeakCommand
	defer func() { defaultSpeakCommand = saved }()

	defaultSpeakCommand = func() string { return "cat >" }
	s := New(Config{Enabled: true})
	data, mime, err := s.Synthesize(context.Background(), "built in")
	if err != nil || string(data) != "built in" || mime != "audio/wav" {
		t.Fatalf("speech=%q mime=%q err=%v", data, mime, err)
	}
	if DefaultSpeechName() == "" {
		t.Error("a default command should have a display name")
	}

	defaultSpeakCommand = func() string { return "" }
	if _, _, err := s.Synthesize(context.Background(), "nothing"); err == nil || !strings.Contains(err.Error(), "audio.tts.command") {
		t.Fatalf("no TTS configured: err=%v", err)
	}
	if DefaultSpeechName() != "" {
		t.Error("no default command should have no display name")
	}
}

func TestSTTWithoutCommandOrEndpointExplainsSetup(t *testing.T) {
	s := New(Config{Enabled: true})
	if _, err := s.Transcribe(context.Background(), []byte("audio"), "recording.wav"); err == nil || !strings.Contains(err.Error(), "audio.stt.command") {
		t.Fatalf("err=%v", err)
	}
}

func TestTailBufferKeepsEnd(t *testing.T) {
	b := &tailBuffer{max: 4}
	b.Write([]byte("abc"))
	b.Write([]byte("defg"))
	if b.String() != "defg" {
		t.Fatalf("tail=%q", b.String())
	}
	l := &limitedBuffer{max: 4}
	l.Write([]byte("abc"))
	l.Write([]byte("defg"))
	if l.String() != "abcd" {
		t.Fatalf("limited=%q", l.String())
	}
}

func TestTranscriptionTextAcceptsPlainText(t *testing.T) {
	if got := transcriptionText([]byte(" hello ")); got != "hello" {
		t.Fatalf("text=%q", got)
	}
}

func TestTranscriptionTextExtractsJSONText(t *testing.T) {
	if got := transcriptionText([]byte(`{"text":" hello ","language":"Indonesian"}`)); got != "hello" {
		t.Fatalf("text=%q", got)
	}
}
