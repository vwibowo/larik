package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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

func TestRecordingReplacesComposerAndPreservesDraft(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 80, 24
	m.opts.Audio = &fakeAudio{transcribed: "run the tests"}
	m.input.SetValue("draft")
	_, start := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl})
	if !strings.Contains(plain(m.composerView()), "Starting microphone") {
		t.Fatal("start should hide the composer")
	}
	m.Update(start().(recordingStartedMsg))
	first := plain(m.composerView())
	if !strings.Contains(first, "Recording") || !strings.Contains(first, "ctrl+space to transcribe") || strings.Contains(first, "draft") {
		t.Fatalf("recording view = %q", first)
	}
	m.Update(voiceTickMsg{id: m.voiceTickID})
	if first == plain(m.composerView()) {
		t.Fatal("waveform should animate between frames")
	}
	m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m.Update(tea.PasteMsg{Content: "invisible paste"})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.input.Value() != "draft" {
		t.Fatalf("hidden draft changed: %q", m.input.Value())
	}
	_, stop := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl})
	if !strings.Contains(plain(m.composerView()), "Transcribing") {
		t.Fatal("transcription state should replace composer")
	}
	m.Update(stop().(transcriptionDoneMsg))
	if m.input.Value() != "draft run the tests" {
		t.Fatalf("transcript was not appended: %q", m.input.Value())
	}
	for range 3 {
		m.Update(voiceTickMsg{id: m.voiceTickID})
	}
	if !strings.Contains(plain(m.composerView()), "draft run the tests") {
		t.Fatal("composer did not return after transcription")
	}
}

func TestRecordingFailureRestoresComposer(t *testing.T) {
	for _, result := range []transcriptionDoneMsg{{err: context.DeadlineExceeded}, {text: " "}} {
		m := testModel(t)
		m.input.SetValue("draft")
		m.busyLabel = "Transcribing…"
		m.Update(result)
		for range 3 {
			m.Update(voiceTickMsg{id: m.voiceTickID})
		}
		if m.input.Value() != "draft" || m.voiceActive() {
			t.Fatalf("failure lost draft or left animation active: %q", m.input.Value())
		}
	}
}

func TestVoiceStartFailureAndStaleTicks(t *testing.T) {
	m := testModel(t)
	m.input.SetValue("draft")
	m.opts.Audio = &fakeAudio{}
	m.busyLabel = "Starting recording…"
	m.Update(recordingStartedMsg{err: context.DeadlineExceeded})
	if m.voiceActive() || !strings.Contains(plain(m.composerView()), "draft") {
		t.Fatal("failed microphone start did not restore draft")
	}
	m.Update(recordingStartedMsg{recording: fakeRecording{data: []byte("audio")}})
	oldID := m.voiceTickID
	m.Update(voiceTickMsg{id: oldID})
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeySpace, Mod: tea.ModCtrl})
	if cmd == nil {
		t.Fatal("stop did not issue a command")
	}
	frame := m.voiceFrame
	m.Update(voiceTickMsg{id: oldID})
	if m.voiceFrame != frame {
		t.Fatal("stale recording tick advanced after stop")
	}
}

func TestVoiceComposerFitsNarrowTerminal(t *testing.T) {
	m := testModel(t)
	m.setWidth(18)
	m.height = 12
	m.recording = fakeRecording{data: []byte("audio")}
	m.voiceStarted = time.Now()
	if got := lipgloss.Width(m.composerView()); got > m.width {
		t.Fatalf("composer width = %d, terminal width = %d", got, m.width)
	}
	if view := plain(m.View().Content); !strings.Contains(view, "▁▂▃") || !strings.Contains(view, "ctrl+space") {
		t.Fatalf("recording indicator or stop key missing on narrow screen: %q", view)
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
