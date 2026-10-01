package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"larik/internal/procgroup"
)

// The status_line setting: a command whose output replaces the footer's
// model, context and cost. It runs again whenever what it's given
// changes, at most every statusMinGap, one run at a time.

const (
	statusMinGap   = 300 * time.Millisecond
	statusTimeout  = 5 * time.Second
	statusMaxLines = 3
)

type statusCmd struct {
	command string
	input   string    // what the latest run was given
	ended   time.Time // when the latest run ended
	running bool
	lines   []string // its output; the default footer shows while empty
	warned  bool     // a failure was reported
}

type statusDoneMsg struct {
	lines []string
	err   error
}

// statusInput is what the command reads on stdin: Claude Code's
// statusLine fields, so its scripts work, and Larik's own under "larik".
type statusInput struct {
	HookEventName  string `json:"hook_event_name"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	Model          struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
	} `json:"model"`
	Workspace struct {
		CurrentDir string `json:"current_dir"`
		ProjectDir string `json:"project_dir"`
	} `json:"workspace"`
	Version     string `json:"version"`
	OutputStyle struct {
		Name string `json:"name"`
	} `json:"output_style"`
	Cost struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	} `json:"cost"`
	ContextWindow struct {
		Size           int `json:"context_window_size"`
		UsedTokens     int `json:"used_tokens"`
		UsedPercentage int `json:"used_percentage"`
	} `json:"context_window"`
	Larik struct {
		Provider        string `json:"provider"`
		Effort          string `json:"effort,omitempty"`
		Mode            string `json:"permission_mode"`
		Execution       string `json:"execution"`
		BackgroundTasks int    `json:"background_tasks"`
		Running         bool   `json:"turn_running"`
	} `json:"larik"`
}

// statusPayload is the command's input for the current state.
func (m *model) statusPayload() string {
	var in statusInput
	in.HookEventName = "Status"
	in.SessionID, in.TranscriptPath = m.agent.SessionID(), m.agent.SessionPath()
	if m.opts.Config != nil {
		in.Cwd = m.opts.Config.Cwd
	}
	in.Model.ID, in.Model.DisplayName = m.agent.Model(), m.agent.Model()
	in.Workspace.CurrentDir, in.Workspace.ProjectDir = in.Cwd, in.Cwd
	in.Version = m.opts.Version
	in.OutputStyle.Name = "default"
	in.Cost.TotalCostUSD = m.stats.CostUSD
	in.ContextWindow.Size, in.ContextWindow.UsedTokens = m.stats.ContextWindow, m.stats.ContextTokens
	if m.stats.ContextWindow > 0 {
		in.ContextWindow.UsedPercentage = min(m.stats.ContextTokens*100/m.stats.ContextWindow, 100)
	}
	in.Larik.Provider = m.agent.ProviderName()
	in.Larik.Effort = string(m.agent.Effort())
	in.Larik.Mode = string(m.agent.Perms().Mode())
	in.Larik.Execution = string(m.agent.Execution())
	in.Larik.BackgroundTasks = m.agent.RunningBackground()
	in.Larik.Running = m.running
	data, _ := json.Marshal(in)
	return string(data)
}

// refreshStatus starts the command when its input has changed since the
// last run and none is running.
func (m *model) refreshStatus() tea.Cmd {
	s := m.status
	if s == nil || s.running {
		return nil
	}
	in := m.statusPayload()
	if in == s.input {
		return nil
	}
	wait := statusMinGap - time.Since(s.ended)
	s.input, s.running = in, true
	command, dir := s.command, ""
	if m.opts.Config != nil {
		dir = m.opts.Config.Cwd
	}
	return func() tea.Msg {
		if wait > 0 {
			time.Sleep(wait)
		}
		lines, err := runStatus(command, dir, in)
		return statusDoneMsg{lines, err}
	}
}

// statusDone takes a run's output. A failure keeps the previous output
// and is reported once.
func (m *model) statusDone(msg statusDoneMsg) tea.Cmd {
	s := m.status
	if s == nil {
		return nil
	}
	s.running, s.ended = false, time.Now()
	if msg.err == nil {
		s.lines = msg.lines
		return nil
	}
	if s.warned {
		return nil
	}
	s.warned = true
	return m.println(m.st.warn.Render("status line: " + msg.err.Error()))
}

func runStatus(command, dir, input string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LARIK_PROJECT_DIR="+dir, "CLAUDE_PROJECT_DIR="+dir)
	cmd.Stdin = strings.NewReader(input)
	procgroup.Configure(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = time.Second // something it started may keep stdout open
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		_ = procgroup.Kill(cmd)
		<-done
		return nil, fmt.Errorf("timed out after %s", statusTimeout)
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		err = nil
	}
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, firstLine(msg))
		}
		return nil, err
	}
	return statusLines(stdout.String()), nil
}

// statusLines keeps the output's first lines, without trailing blanks.
func statusLines(out string) []string {
	lines := strings.Split(strings.TrimRight(out, "\r\n\t "), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return nil
	}
	if len(lines) > statusMaxLines {
		lines = lines[:statusMaxLines]
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\r")
	}
	return lines
}

// customStatus is the footer with the command's output: the mode chip,
// which is always shown, then its first line; further lines below.
func (m *model) customStatus(modeChip string) string {
	lines := make([]string, len(m.status.lines))
	for i, l := range m.status.lines {
		if i == 0 {
			l = modeChip + " " + l
		}
		lines[i] = ansi.Truncate(l, max(m.width, 1), "…")
	}
	return strings.Join(lines, "\n")
}
