// Package permission decides whether a tool call may run.
//
// Rules look like `tool` or `tool(pattern)`: `bash(git status*)`,
// `edit(src/**)`, `read`. Bash patterns match the command string with `*`
// as a wildcard; file-tool patterns are globs on the path relative to the
// working directory. MCP tools are matched by name (`mcp__github__get_issue`)
// or by server (`mcp__github` covers every tool of that server). Deny rules
// always win.
package permission

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"
)

type Mode string

const (
	ModeDefault     Mode = "default"      // ask before edits and commands
	ModeAcceptEdits Mode = "accept-edits" // edits inside cwd run freely; commands ask
	ModePlan        Mode = "plan"         // read-only tools only
	ModeYolo        Mode = "yolo"         // everything runs
)

func ParseMode(s string) (Mode, error) {
	switch m := Mode(s); m {
	case ModeDefault, ModeAcceptEdits, ModePlan, ModeYolo:
		return m, nil
	case "":
		return ModeDefault, nil
	}
	return "", fmt.Errorf("unknown mode %q (default, accept-edits, plan, yolo)", s)
}

type Decision int

const (
	Allow Decision = iota
	Ask
	Deny
)

type Rules struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

type Checker struct {
	*state
	cwd string // relative paths and path rules resolve against it
}

// state is shared by a checker and those derived from it with WithCwd,
// so mode changes and "always allow" answers apply to all of them.
type state struct {
	mu    sync.Mutex
	mode  Mode
	rules Rules
	// sandboxed: bash runs confined by the OS sandbox, so sandboxed
	// commands need no prompt; opting out with "sandbox": false does.
	sandboxed bool
}

// SetSandboxed records whether bash commands run in the OS sandbox.
func (c *Checker) SetSandboxed(on bool) {
	c.mu.Lock()
	c.sandboxed = on
	c.mu.Unlock()
}

// Sandboxed reports whether the sandbox is active.
func (c *Checker) Sandboxed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sandboxed
}

// wantsSandbox reports whether a bash call runs sandboxed (the default).
func wantsSandbox(input json.RawMessage) bool {
	var in struct {
		Sandbox *bool `json:"sandbox"`
	}
	_ = json.Unmarshal(input, &in)
	return in.Sandbox == nil || *in.Sandbox
}

func NewChecker(mode Mode, rules Rules, cwd string) *Checker {
	return &Checker{state: &state{mode: mode, rules: rules}, cwd: cwd}
}

// WithCwd returns a checker for another directory (e.g. a subagent's git
// worktree) that shares this one's mode and rules.
func (c *Checker) WithCwd(cwd string) *Checker {
	return &Checker{state: c.state, cwd: cwd}
}

func (c *Checker) Mode() Mode {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mode
}

func (c *Checker) SetMode(m Mode) {
	c.mu.Lock()
	c.mode = m
	c.mu.Unlock()
}

// AddAllow adds a session rule (e.g. from an "always allow" answer).
func (c *Checker) AddAllow(rule string) {
	c.mu.Lock()
	c.rules.Allow = append(c.rules.Allow, rule)
	c.mu.Unlock()
}

// Call describes a tool invocation for permission purposes.
type Call struct {
	Tool     string
	ReadOnly bool
	Input    json.RawMessage
}

// Subject is the string rules match against: the command for bash, the
// path for file tools, empty otherwise.
func Subject(tool string, input json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
		Path    string `json:"path"`
		URL     string `json:"url"`
	}
	_ = json.Unmarshal(input, &in)
	switch tool {
	case "bash":
		return in.Command
	case "web_fetch":
		return urlHost(in.URL)
	}
	return in.Path
}

func urlHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// networkTools reach the internet but change nothing locally: plan mode
// asks for them instead of denying, since research is what planning needs.
var networkTools = map[string]bool{"web_fetch": true, "web_search": true}

// Decide returns the decision and, for Deny, a reason to show the model.
func (c *Checker) Decide(call Call) (Decision, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	subject := Subject(call.Tool, call.Input)

	for _, r := range c.rules.Deny {
		if c.matches(r, call.Tool, subject) {
			return Deny, fmt.Sprintf("denied by rule %q", r)
		}
	}
	if c.mode == ModeYolo {
		return Allow, ""
	}
	if call.ReadOnly {
		return Allow, ""
	}
	if c.mode == ModePlan && !networkTools[call.Tool] {
		return Deny, "plan mode is active: only read-only tools may run. Present your plan instead of making changes."
	}
	for _, r := range c.rules.Allow {
		if c.matches(r, call.Tool, subject) {
			return Allow, ""
		}
	}
	switch call.Tool {
	case "bash":
		if c.sandboxed && wantsSandbox(call.Input) {
			return Allow, "" // confined by the OS sandbox
		}
		if safeCommand(subject) {
			return Allow, ""
		}
	case "write", "edit":
		if c.mode == ModeAcceptEdits && c.insideCwd(subject) {
			return Allow, ""
		}
	}
	return Ask, ""
}

// SuggestRule proposes an "always allow" rule for a call: the command's
// first word(s) for bash, the tool name for everything else.
func SuggestRule(tool string, input json.RawMessage) string {
	if tool == "web_fetch" {
		if host := Subject(tool, input); host != "" {
			return "web_fetch(domain:" + host + ")"
		}
	}
	if tool != "bash" {
		return tool
	}
	fields := strings.Fields(Subject(tool, input))
	switch {
	case len(fields) == 0:
		return "bash"
	case len(fields) >= 2 && !strings.HasPrefix(fields[1], "-") && subcommandTools[fields[0]]:
		return fmt.Sprintf("bash(%s %s*)", fields[0], fields[1])
	default:
		return fmt.Sprintf("bash(%s*)", fields[0])
	}
}

var subcommandTools = map[string]bool{"git": true, "go": true, "npm": true, "pnpm": true, "yarn": true, "cargo": true, "docker": true, "kubectl": true, "make": true, "uv": true, "bun": true}

var ruleRe = regexp.MustCompile(`^([\w-]+)(?:\((.*)\))?$`)

func (c *Checker) matches(rule, tool, subject string) bool {
	m := ruleRe.FindStringSubmatch(strings.TrimSpace(rule))
	if m == nil {
		return false
	}
	if m[1] != tool {
		// `mcp__server` covers all of that server's tools.
		return strings.HasPrefix(m[1], "mcp__") && !strings.Contains(m[1][len("mcp__"):], "__") &&
			strings.HasPrefix(tool, m[1]+"__") && m[2] == ""
	}
	pattern := m[2]
	if pattern == "" || pattern == "*" {
		return true
	}
	if tool == "bash" {
		return wildcard(pattern, strings.TrimSpace(subject))
	}
	if tool == "web_fetch" {
		// web_fetch(domain:go.dev) also covers subdomains like pkg.go.dev.
		d, ok := strings.CutPrefix(pattern, "domain:")
		d = strings.TrimPrefix(strings.ToLower(d), "*.")
		return ok && d != "" && (subject == d || strings.HasSuffix(subject, "."+d))
	}
	rel := subject
	if abs := c.abs(subject); c.insideCwd(subject) {
		rel, _ = filepath.Rel(c.cwd, abs)
	}
	ok, _ := doublestar.Match(pattern, filepath.ToSlash(rel))
	return ok
}

func (c *Checker) abs(p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(c.cwd, p)
}

func (c *Checker) insideCwd(p string) bool {
	rel, err := filepath.Rel(c.cwd, c.abs(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// wildcard matches s against a pattern where * matches any run of characters.
func wildcard(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// safeCommands never modify state. They are auto-allowed only when the
// command has no shell operators that could chain in something else.
var safeCommands = []string{
	"ls", "pwd", "cat", "head", "tail", "wc", "file", "which", "echo", "date", "tree", "du", "df",
	"git status", "git diff", "git log", "git show", "git branch", "git rev-parse", "git blame",
	"go version", "go env", "go list", "go vet", "node --version", "python --version", "python3 --version",
}

func safeCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" || strings.ContainsAny(cmd, ";&|<>$`\n(){}") {
		return false
	}
	for _, s := range safeCommands {
		if cmd == s || strings.HasPrefix(cmd, s+" ") {
			return true
		}
	}
	return false
}
