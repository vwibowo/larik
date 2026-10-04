package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/audio"
)

type recordingStartedMsg struct {
	recording audio.Recording
	err       error
}
type transcriptionDoneMsg struct {
	text string
	err  error
}
type speechDoneMsg struct{ err error }

type voiceTickMsg struct{ id int }

func (m *model) voiceActive() bool {
	return m.recording != nil || m.busyLabel == "Starting recording…" || m.busyLabel == "Transcribing…" || m.voiceReturn > 0
}

func (m *model) voiceTick() tea.Cmd {
	id := m.voiceTickID
	return tea.Tick(110*time.Millisecond, func(time.Time) tea.Msg { return voiceTickMsg{id: id} })
}

func (m *model) startRecording() tea.Cmd {
	if m.opts.Audio == nil {
		return m.println(m.st.err.Render("audio is not enabled; configure audio in personal settings"))
	}
	if m.busyLabel == "Starting recording…" || m.busyLabel == "Transcribing…" || m.voiceReturn > 0 {
		return nil
	}
	m.busyLabel = "Starting recording…"
	svc := m.opts.Audio
	return func() tea.Msg {
		r, err := svc.StartRecording(context.Background())
		return recordingStartedMsg{recording: r, err: err}
	}
}

func (m *model) stopRecording() tea.Cmd {
	r := m.recording
	m.recording = nil
	m.busyLabel = "Transcribing…"
	m.voiceTickID++ // stop the decorative recording animation
	svc := m.opts.Audio
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		data, err := r.Stop(ctx)
		if err == nil {
			var text string
			text, err = svc.Transcribe(ctx, data, "recording.wav")
			return transcriptionDoneMsg{text: text, err: err}
		}
		return transcriptionDoneMsg{err: err}
	}
}

func (m *model) toggleRecording() tea.Cmd {
	if m.recording != nil {
		return m.stopRecording()
	}
	return m.startRecording()
}

func (m *model) speak(text string) tea.Cmd {
	text = strings.TrimSpace(text)
	if text == "" {
		return m.println(m.st.dim.Render("There is no assistant reply to speak yet"))
	}
	if m.opts.Audio == nil {
		return m.println(m.st.err.Render("audio is not enabled; configure audio in personal settings"))
	}
	m.busyLabel = "Speaking…"
	svc := m.opts.Audio
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		data, mime, err := svc.Synthesize(ctx, text)
		if err == nil {
			err = svc.Play(ctx, data, mime)
		}
		return speechDoneMsg{err: err}
	}
}

func audioError(prefix string, err error) tea.Cmd {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return func() tea.Msg { return outputMsg(fmt.Sprintf("%s: %v", prefix, err)) }
}
