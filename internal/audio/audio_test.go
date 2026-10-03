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
