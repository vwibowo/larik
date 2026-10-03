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
	"larik/internal/procgroup"
)

// The status_line setting: a command whose output is shown on the left side
// of the footer. It runs again whenever what it's given changes, at most every
// statusMinGap, one run at a time.

const (
	statusMinGap    = 300 * time.Millisecond
	statusTimeout   = 5 * time.Second
	statusMaxLines  = 3
	sidebarMaxLines = 12
	statusMaxOutput = 16 << 10
)

type statusCmd struct {
	command  string
	interval time.Duration
	input    string    // what the latest run was given
	ended    time.Time // when the latest run ended
	running  bool
	lines    []string // its output; the default display shows while empty
	warned   bool     // a failure was reported
}

type statusDoneMsg struct {
	lines   []string
	err     error
	sidebar bool
}

type statusTickMsg struct{}

func newStatusCmd(command string, intervalMS int) *statusCmd {
	interval := time.Second
	if intervalMS > 0 {
		interval = time.Duration(intervalMS) * time.Millisecond
	}
	return &statusCmd{command: command, interval: interval}
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
		Activity        string `json:"activity"`
		ActiveTool      string `json:"active_tool,omitempty"`
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
	in.Larik.ActiveTool = m.calling
	switch {
	case m.calling != "":
		in.Larik.Activity = "working"
	case m.stream.Len() > 0:
		in.Larik.Activity = "responding"
	case m.thinking.Len() > 0:
		in.Larik.Activity = "thinking"
	case m.running:
		in.Larik.Activity = "working"
	default:
		in.Larik.Activity = "idle"
	}
	data, _ := json.Marshal(in)
	return string(data)
}

// refreshStatus starts the command when its input has changed since the
// last run and none is running.
func (m *model) refreshStatus() tea.Cmd {
	var cmds []tea.Cmd
	for _, target := range []struct {
		state   *statusCmd
		sidebar bool
	}{{m.status, false}, {m.sidebarStatus, true}} {
		s := target.state
		if s == nil || s.running || target.sidebar && !m.showInfo {
			continue
		}
		in := m.statusPayload()
		if in == s.input && (s.interval <= 0 || time.Since(s.ended) < s.interval) {
			continue
		}
		wait := statusMinGap - time.Since(s.ended)
		s.input, s.running = in, true
		command, dir, isSidebar := s.command, "", target.sidebar
		if m.opts.Config != nil {
			dir = m.opts.Config.Cwd
		}
		maxLines := statusMaxLines
		if isSidebar {
			maxLines = sidebarMaxLines
		}
		cmds = append(cmds, func() tea.Msg {
			if wait > 0 {
				time.Sleep(wait)
			}
			lines, err := runStatusLimit(command, dir, in, maxLines)
			return statusDoneMsg{lines: lines, err: err, sidebar: isSidebar}
		})
	}
	return tea.Batch(cmds...)
}

func (m *model) statusTick() tea.Cmd {
	if m.status == nil && m.sidebarStatus == nil {
		return nil
	}
	interval := time.Second
	if m.status != nil {
		interval = min(interval, m.status.interval)
	}
	if m.sidebarStatus != nil {
		interval = min(interval, m.sidebarStatus.interval)
	}
	return tea.Tick(interval, func(time.Time) tea.Msg { return statusTickMsg{} })
}

// statusDone takes a run's output. A failure keeps the previous output
// and is reported once.
func (m *model) statusDone(msg statusDoneMsg) tea.Cmd {
	s, label := m.status, "status line"
	if msg.sidebar {
		s, label = m.sidebarStatus, "sidebar"
	}
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
	return m.println(m.st.warn.Render(label + ": " + msg.err.Error()))
}

func runStatus(command, dir, input string) ([]string, error) {
	return runStatusLimit(command, dir, input, statusMaxLines)
}

func runStatusLimit(command, dir, input string, maxLines int) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LARIK_PROJECT_DIR="+dir, "CLAUDE_PROJECT_DIR="+dir)
	cmd.Stdin = strings.NewReader(input)
	procgroup.Configure(cmd)
	stdout, stderr := &limitedBuffer{limit: statusMaxOutput}, &limitedBuffer{limit: 4096}
	cmd.Stdout, cmd.Stderr = stdout, stderr
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
	return statusLinesLimit(stdout.String(), maxLines), nil
}

// statusLines keeps the output's first lines, without trailing blanks.
func statusLines(out string) []string {
	return statusLinesLimit(out, statusMaxLines)
}

func statusLinesLimit(out string, limit int) []string {
	lines := strings.Split(strings.TrimRight(out, "\r\n\t "), "\n")
	if len(lines) == 1 && strings.TrimSpace(lines[0]) == "" {
		return nil
	}
	if len(lines) > limit {
		lines = lines[:limit]
	}
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, "\r")
	}
	return lines
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if remaining := b.limit - b.Len(); remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return n, nil
}
