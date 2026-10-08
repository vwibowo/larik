// Package audio provides local speech input and output through an
// OpenAI-compatible audio API or local commands (such as whisper.cpp's
// whisper-cli, or macOS say). It deliberately knows nothing about the agent
// loop or the TUI.
package audio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	ErrDisabled   = errors.New("audio is disabled")
	ErrNoRecorder = errors.New("audio recording is unavailable")
)

type Recording interface {
	Stop(context.Context) ([]byte, error)
}

type Service interface {
	StartRecording(context.Context) (Recording, error)
	Transcribe(context.Context, []byte, string) (string, error)
	Synthesize(context.Context, string) ([]byte, string, error)
	Play(context.Context, []byte, string) error
	SetSTTLanguage(string)
}

type Config struct {
	Enabled            bool           `json:"enabled,omitempty"`
	STT                EndpointConfig `json:"stt,omitempty"`
	TTS                EndpointConfig `json:"tts,omitempty"`
	RecordCommand      string         `json:"record_command,omitempty"`
	PlayCommand        string         `json:"play_command,omitempty"`
	MaxDurationSeconds int            `json:"max_duration_seconds,omitempty"`
	AutoSpeak          bool           `json:"auto_speak,omitempty"`
}

type EndpointConfig struct {
	// Command, when set, replaces the HTTP endpoint: Larik runs it with sh -c
	// and appends a WAV path as the final argument. An STT command reads that
	// file and prints the transcript; a TTS command reads text on stdin and
	// writes speech to that file.
	Command   string `json:"command,omitempty"`
	BaseURL   string `json:"base_url,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	APIKeyEnv string `json:"api_key_env,omitempty"`
	Model     string `json:"model,omitempty"`
	Language  string `json:"language,omitempty"`
	Voice     string `json:"voice,omitempty"`
}

type OpenAICompatible struct {
	cfg      Config
	http     *http.Client
	recorder *commandRecorder
	player   *commandPlayer
}

func New(cfg Config) *OpenAICompatible {
	if cfg.MaxDurationSeconds <= 0 {
		cfg.MaxDurationSeconds = 120
	}
	return &OpenAICompatible{cfg: cfg, http: &http.Client{Timeout: 10 * time.Minute}, recorder: newCommandRecorder(cfg.RecordCommand, cfg.MaxDurationSeconds), player: newCommandPlayer(cfg.PlayCommand)}
}
func (s *OpenAICompatible) SetSTTLanguage(language string) {
	s.cfg.STT.Language = language
}

func (s *OpenAICompatible) StartRecording(ctx context.Context) (Recording, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	return s.recorder.Start(ctx)
}
func (s *OpenAICompatible) Transcribe(ctx context.Context, data []byte, name string) (string, error) {
	if !s.cfg.Enabled {
		return "", ErrDisabled
	}
	if s.cfg.STT.Command != "" {
		return s.transcribeCommand(ctx, data)
	}
	if s.cfg.STT.BaseURL == "" || s.cfg.STT.Model == "" {
		return "", errors.New("configure audio.stt.command, or audio.stt.base_url and model")
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filepath.Base(name))
	if err != nil {
		return "", err
	}
	if _, err = part.Write(data); err != nil {
		return "", err
	}
	_ = mw.WriteField("model", s.cfg.STT.Model)
	if s.cfg.STT.Language != "" {
		_ = mw.WriteField("language", s.cfg.STT.Language)
	}
	_ = mw.WriteField("response_format", "text")
	if err = mw.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(s.cfg.STT.BaseURL, "/audio/transcriptions"), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	setAuth(req, s.cfg.STT)
	res, err := s.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return "", fmt.Errorf("STT server returned %s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", err
	}
	return transcriptionText(b), nil
}

// transcribeCommand runs the STT command on a temporary WAV file and reads
// the transcript from its stdout.
func (s *OpenAICompatible) transcribeCommand(ctx context.Context, data []byte) (string, error) {
	f, err := os.CreateTemp("", "larik-stt-*.wav")
	if err != nil {
		return "", err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	out, err := runCommand(ctx, s.cfg.STT.Command, path, nil, "LARIK_STT_LANGUAGE="+s.cfg.STT.Language)
	if err != nil {
		return "", fmt.Errorf("STT command failed: %w", err)
	}
	text := transcriptionText(out)
	if text == "" {
		return "", errors.New("STT command produced no text")
	}
	return text, nil
}

// transcriptionText accepts both the OpenAI text response and compatible
// servers that return a JSON transcription object despite response_format=text.
func transcriptionText(data []byte) string {
	text := strings.TrimSpace(string(data))
	var response struct {
		Text string `json:"text"`
	}
	if strings.HasPrefix(text, "{") && json.Unmarshal(data, &response) == nil && response.Text != "" {
		return strings.TrimSpace(response.Text)
	}
	return text
}
func (s *OpenAICompatible) Synthesize(ctx context.Context, text string) ([]byte, string, error) {
	if !s.cfg.Enabled {
		return nil, "", ErrDisabled
	}
	if s.cfg.TTS.Command != "" {
		return s.synthesizeCommand(ctx, s.cfg.TTS.Command, text)
	}
	if s.cfg.TTS.BaseURL == "" {
		if command := defaultSpeakCommand(); command != "" {
			return s.synthesizeCommand(ctx, command, text)
		}
		return nil, "", errors.New("configure audio.tts.command, or audio.tts.base_url and model")
	}
	if s.cfg.TTS.Model == "" {
		return nil, "", errors.New("TTS base_url and model must be configured")
	}
	payload := map[string]string{"model": s.cfg.TTS.Model, "input": text, "voice": s.cfg.TTS.Voice, "response_format": "wav"}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint(s.cfg.TTS.BaseURL, "/audio/speech"), bytes.NewReader(b))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	setAuth(req, s.cfg.TTS)
	res, err := s.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		e, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
		return nil, "", fmt.Errorf("TTS server returned %s: %s", res.Status, strings.TrimSpace(string(e)))
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	return data, res.Header.Get("Content-Type"), err
}

// synthesizeCommand pipes text to the TTS command, which writes speech to the
// WAV path appended to it.
func (s *OpenAICompatible) synthesizeCommand(ctx context.Context, command, text string) ([]byte, string, error) {
	f, err := os.CreateTemp("", "larik-tts-*.wav")
	if err != nil {
		return nil, "", err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if _, err = runCommand(ctx, command, path, strings.NewReader(text), "LARIK_TTS_VOICE="+s.cfg.TTS.Voice); err != nil {
		return nil, "", fmt.Errorf("TTS command failed: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	if len(data) == 0 {
		return nil, "", errors.New("TTS command produced no audio")
	}
	return data, "audio/wav", nil
}

// runCommand runs command with path appended as its final argument and
// returns stdout (capped at 1 MB). A failure carries the end of stderr.
func runCommand(ctx context.Context, command, path string, stdin io.Reader, env ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command+" "+shellQuote(path))
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = stdin
	stdout := &limitedBuffer{max: 1 << 20}
	stderr := &tailBuffer{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%v: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// limitedBuffer keeps the first max bytes written and discards the rest.
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		b.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// tailBuffer keeps the last max bytes written.
type tailBuffer struct {
	buf []byte
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.buf = append(b.buf, p...)
	if over := len(b.buf) - b.max; over > 0 {
		b.buf = append(b.buf[:0], b.buf[over:]...)
	}
	return len(p), nil
}

func (b *tailBuffer) String() string { return string(b.buf) }

func (s *OpenAICompatible) Play(ctx context.Context, data []byte, mime string) error {
	if !s.cfg.Enabled {
		return ErrDisabled
	}
	return s.player.Play(ctx, data, mime)
}
func endpoint(base, suffix string) string {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return strings.TrimRight(base, "/") + suffix
	}
	u.Path = strings.TrimRight(u.Path, "/") + suffix
	return u.String()
}
func setAuth(r *http.Request, c EndpointConfig) {
	key := c.APIKey
	if key == "" && c.APIKeyEnv != "" {
		key = os.Getenv(c.APIKeyEnv)
	}
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
}

type commandRecorder struct {
	command string
	max     time.Duration
}
type commandRecording struct {
	cmd  *exec.Cmd
	path string
}

func newCommandRecorder(command string, seconds int) *commandRecorder {
	return &commandRecorder{command: command, max: time.Duration(seconds) * time.Second}
}
func (r *commandRecorder) Start(ctx context.Context) (Recording, error) {
	if _, err := exec.LookPath("ffmpeg"); err != nil && r.command == "" {
		return nil, fmt.Errorf("%w: install ffmpeg or set audio.record_command", ErrNoRecorder)
	}
	f, err := os.CreateTemp("", "larik-record-*.wav")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()
	command := r.command
	if command == "" {
		command = defaultRecordCommand()
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", command+" "+shellQuote(path))
	if err := cmd.Start(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return &commandRecording{cmd: cmd, path: path}, nil
}
func (r *commandRecording) Stop(ctx context.Context) ([]byte, error) {
	if r.cmd.Process != nil {
		_ = r.cmd.Process.Signal(os.Interrupt)
	}
	done := make(chan error, 1)
	go func() { done <- r.cmd.Wait() }()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
	}
	defer os.Remove(r.path)
	return os.ReadFile(r.path)
}

type commandPlayer struct{ command string }

func newCommandPlayer(command string) *commandPlayer { return &commandPlayer{command: command} }
func (p *commandPlayer) Play(ctx context.Context, data []byte, _ string) error {
	f, err := os.CreateTemp("", "larik-speak-*.wav")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	f.Close()
	command := p.command
	if command == "" {
		command = defaultPlayCommand()
	}
	return exec.CommandContext(ctx, "sh", "-c", command+" "+shellQuote(path)).Run()
}
func defaultRecordCommand() string {
	if runtime.GOOS == "darwin" {
		return "ffmpeg -loglevel error -f avfoundation -i :0 -ar 16000 -ac 1 -y"
	}
	return "ffmpeg -loglevel error -f pulse -i default -ar 16000 -ac 1 -y"
}
func defaultPlayCommand() string {
	if runtime.GOOS == "darwin" {
		return "afplay"
	}
	return "ffplay -nodisp -autoexit -loglevel quiet"
}

// defaultSpeakCommand is the TTS used when no endpoint or command is set: the
// built-in macOS voice. It is a variable so tests need not produce real audio.
var defaultSpeakCommand = func() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	if _, err := exec.LookPath("say"); err != nil {
		return ""
	}
	return `say ${LARIK_TTS_VOICE:+-v "$LARIK_TTS_VOICE"} --file-format=WAVE --data-format=LEI16@22050 -o`
}

// DefaultSpeechName names the built-in TTS for display, or "" when there is none.
func DefaultSpeechName() string {
	if defaultSpeakCommand() == "" {
		return ""
	}
	return "say (built-in)"
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
