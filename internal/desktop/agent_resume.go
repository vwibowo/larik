package desktop

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// agentResume contains only the identity needed to offer an explicit
// continuation. It never holds a prompt, tool input, or transcript.
type agentResume struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Tab  string `json:"tab"`
	Dir  string `json:"dir"`
}

func agentForTab(t *tab, r agentResume) agentResume {
	if name, _, ok := splitRemote(t.dir); ok {
		if r.Dir != "" && !isRemote(r.Dir) {
			r.Dir = remotePath(name, r.Dir)
		}
		if other, _, remote := splitRemote(r.Dir); !remote || other != name {
			return agentResume{}
		}
	} else if isRemote(r.Dir) {
		return agentResume{}
	}
	if r.Tab != t.id || !r.valid() {
		return agentResume{}
	}
	return r
}

func validAgentSessionID(id string) bool {
	if len(id) < 6 || len(id) > 128 {
		return false
	}
	for _, c := range []byte(id) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (r agentResume) valid() bool {
	_, path, remote := splitRemote(r.Dir)
	if !remote {
		path = r.Dir
	}
	if !validAgentSessionID(r.ID) || r.Tab == "" || !filepath.IsAbs(path) || strings.IndexByte(path, 0) >= 0 {
		return false
	}
	switch r.Kind {
	case "claude", "codex", "opencode", "gemini":
		return true
	}
	return false
}

func (r agentResume) command(checkLocal bool) (string, error) {
	if !r.valid() {
		return "", errors.New("agent session ID is unavailable or invalid")
	}
	var name, command string
	switch r.Kind {
	case "claude":
		name, command = "claude", "claude --resume "+r.ID
	case "codex":
		name, command = "codex", "codex resume "+r.ID
	case "opencode":
		name, command = "opencode", "opencode --session "+r.ID
	case "gemini":
		name, command = "gemini", "gemini --resume "+r.ID
	}
	if checkLocal {
		if _, err := exec.LookPath(name); err != nil {
			return "", fmt.Errorf("%s is not installed: %w", name, err)
		}
	}
	return command, nil
}

func (r agentResume) checkRemote(a *App) error {
	name, _, ok := splitRemote(r.Dir)
	if !ok {
		return nil
	}
	srv := a.serverNamed(name)
	if srv == nil || a.dialOf(srv) == nil {
		return fmt.Errorf("not connected to %s", name)
	}
	copy := r
	_, copy.Dir, _ = splitRemote(copy.Dir)
	conn, _, _, err := askHostVia(a.dialOf(srv), hostRequest{Op: "agent-check", Agent: &copy})
	if conn != nil {
		conn.Close()
	}
	return err
}

func (a *App) resumeAgent(t *tab) {
	if !(t.exited || t.agentOffered) || !t.agent.valid() || t.agent.Tab != t.id {
		return
	}
	command, err := t.agent.command(!isRemote(t.dir))
	if err == nil && isRemote(t.dir) {
		err = t.agent.checkRemote(a)
	}
	if err != nil {
		a.notice = err.Error()
		return
	}
	oldDir, oldRun, oldTerm := t.dir, t.run, t.term
	t.dir, t.run = t.agent.Dir, command
	if !a.run(t, "") {
		t.dir, t.run = oldDir, oldRun
		return
	}
	t.exited, t.code = false, 0
	t.agentOffered = false
	if oldTerm != nil {
		oldTerm.Close()
	}
	a.activate(t)
}

func (a *App) declineAgent(t *tab) {
	if !(t.exited || t.agentOffered) || !t.agent.valid() {
		return
	}
	t.agent = agentResume{}
	t.agentOffered = false
	t.run = ""
	if t.exited {
		a.restart(t)
	}
}
