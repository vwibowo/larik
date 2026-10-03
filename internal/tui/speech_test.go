package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/audio"
)

type fakeRecording struct{ data []byte }

func (r fakeRecording) Stop(context.Context) ([]byte, error) { return r.data, nil }

type fakeAudio struct {
	recording   bool
	transcribed string
	spoken      string
	language    string
}

func (f *fakeAudio) StartRecording(context.Context) (audio.Recording, error) {
	f.recording = true
	return fakeRecording{[]byte("audio")}, nil
}
func (f *fakeAudio) Transcribe(context.Context, []byte, string) (string, error) {
	return f.transcribed, nil
}
func (f *fakeAudio) Synthesize(context.Context, string) ([]byte, string, error) {
	f.spoken = "synthesized"
	return []byte("wav"), "audio/wav", nil
}
func (f *fakeAudio) Play(context.Context, []byte, string) error { return nil }
func (f *fakeAudio) SetSTTLanguage(language string)             { f.language = language }

func TestRecordAudioKeyDispatchesToService(t *testing.T) {
	m := testModel(t)
	f := &fakeAudio{transcribed: "run the tests"}
	m.opts.Audio = f

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("ctrl+space should start recording")
	}
	msg := cmd()
	started, ok := msg.(recordingStartedMsg)
	if !ok {
		t.Fatalf("message type = %T, want recordingStartedMsg", msg)
	}
	if started.err != nil || started.recording == nil {
		t.Fatalf("recording start = %#v", started)
	}
	m.Update(started)
	if m.recording == nil || !f.recording {
		t.Fatal("recording was not installed after ctrl+space")
	}
}

func TestSTTLanguageCommandSwitchesLanguage(t *testing.T) {
	m := testModel(t)
	f := &fakeAudio{}
	m.opts.Audio = f
	var message string
	info := func(s string) tea.Cmd { message = s; return nil }
	fail := func(s string) tea.Cmd { t.Fatalf("unexpected failure: %s", s); return nil }

	if cmd := m.sttLanguageCommand("english", info, fail); cmd != nil {
		cmd()
	}
	if got := m.opts.Config.Audio.STT.Language; got != "en" || f.language != "en" {
		t.Fatalf("language config=%q service=%q", got, f.language)
	}
	if !strings.Contains(message, "en") {
		t.Fatalf("message=%q", message)
	}
	m.sttLanguageCommand("indonesia", info, fail)
	if got := m.opts.Config.Audio.STT.Language; got != "id" || f.language != "id" {
		t.Fatalf("language config=%q service=%q", got, f.language)
	}
}

func TestRecordingIndicatorShowsMicrophoneAndStopHint(t *testing.T) {
	m := testModel(t)
	m.width = 80
	m.height = 24
	m.recording = fakeRecording{data: []byte("audio")}
	line := plain(m.liveView(80))
	if !strings.Contains(line, "🎙 Recording") {
		t.Fatalf("recording indicator = %q", line)
	}
	if !strings.Contains(line, "ctrl+space to transcribe") {
		t.Fatalf("recording indicator lacks stop hint: %q", line)
	}
}

func TestAudioServiceCanBeInjected(t *testing.T) {
	f := &fakeAudio{transcribed: "run the tests"}
	if _, err := f.StartRecording(context.Background()); err != nil {
		t.Fatal(err)
	}
	text, err := f.Transcribe(context.Background(), []byte("audio"), "recording.wav")
	if err != nil || text != "run the tests" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	_, _, _ = f.Synthesize(context.Background(), "hello")
	if f.spoken != "synthesized" {
		t.Fatal("TTS was not called")
	}
}
