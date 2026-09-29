// Package subagent lets the main agent delegate work to child agents with
// their own context window, system prompt and tool set, via the task tool.
//
// Definitions use the Claude Code format: <name>.md with frontmatter
// (name, description, tools, model) whose body is the system prompt.
package subagent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"larik/internal/skills"
	"larik/internal/tools"
)

type Definition struct {
	Name        string
	Description string
	Prompt      string   // system prompt body
	Tools       []string // allowed tool names; nil means all (except task)
	Model       string   // "", "inherit", a role (worker, explore, smart, opus, …) or provider/model
	Isolation   string   // "worktree" to always run in a git worktree
	Source      string   // "builtin" or the file path
}

var builtins = []Definition{
	{
		Name:        "general-purpose",
		Description: "General agent for multi-step research or implementation tasks that would otherwise fill your context: investigating a question across many files, or making a self-contained change.",
		Prompt: "You are a subagent handling a task delegated by the main agent. Work autonomously with your tools until the task is done. " +
			"Stay within the task's scope, match the surrounding code style, and verify changes when tests or builds exist.",
		Model:  "worker",
		Source: "builtin",
	},
	{
		Name:        "explore",
		Description: "Fast read-only agent for finding code, files and facts in the codebase. Use it for broad searches when you only need the conclusion, not the file contents.",
		Prompt: "You are a read-only search subagent. Find what the task asks about using read, grep and glob (and lsp for definitions and references, when available). Never modify anything. " +
			"Search broadly first, then read only the relevant parts. Report findings concisely with path:line references.",
		Tools:  []string{"read", "grep", "glob", "lsp"},
		Model:  "explore",
		Source: "builtin",
	},
}

// Footer is appended to every subagent system prompt.
const footer = "When you finish, reply with a final message that fully answers the task. It is returned verbatim to the agent that delegated to you, " +
	"which cannot see your tool calls or intermediate messages: include the key findings, relevant file paths, and exactly what you changed, if anything."

type Set struct {
	byName   map[string]Definition
	Warnings []string
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Tools       any    `yaml:"tools"` // "Read, Grep" or [Read, Grep]
	Model       string `yaml:"model"`
	Isolation   string `yaml:"isolation"`
}

// Dirs lists definition directories in increasing precedence.
func Dirs(home, configDir, cwd, repoRoot string) []string {
	dirs := []string{
		filepath.Join(home, ".claude", "agents"),
		filepath.Join(configDir, "agents"),
	}
	var chain []string
	for d := cwd; ; d = filepath.Dir(d) {
		chain = append(chain, d)
		if repoRoot == "" || d == repoRoot || d == filepath.Dir(d) {
			break
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		dirs = append(dirs, filepath.Join(chain[i], ".claude", "agents"), filepath.Join(chain[i], ".larik", "agents"))
	}
	return dirs
}

// Discover loads the built-ins and then definitions from dirs; later
// definitions override earlier ones with the same name.
func Discover(dirs []string) *Set {
	s := &Set{byName: map[string]Definition{}}
	for _, d := range builtins {
		s.byName[d.Name] = d
	}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			def, err := parse(path)
			if err != nil {
				s.Warnings = append(s.Warnings, err.Error())
				continue
			}
			s.byName[def.Name] = def
		}
	}
	return s
}

func parse(path string) (Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, err
	}
	fm, body, err := skills.SplitFrontmatter(data)
	if err != nil {
		return Definition{}, fmt.Errorf("%s: %v", path, err)
	}
	var meta frontmatter
	if err := yaml.Unmarshal(fm, &meta); err != nil {
		return Definition{}, fmt.Errorf("%s: invalid frontmatter: %v", path, err)
	}
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(path), ".md")
	}
	if strings.ContainsAny(name, " \t/") {
		return Definition{}, fmt.Errorf("%s: invalid agent name %q", path, name)
	}
	desc := strings.Join(strings.Fields(meta.Description), " ")
	if desc == "" {
		return Definition{}, fmt.Errorf("%s: description is required", path)
	}
	def := Definition{Name: name, Description: desc, Prompt: strings.TrimSpace(string(body)), Model: strings.TrimSpace(meta.Model), Isolation: strings.TrimSpace(meta.Isolation), Source: path}
	switch t := meta.Tools.(type) {
	case string:
		for _, n := range strings.Split(t, ",") {
			if n = strings.TrimSpace(n); n != "" {
				def.Tools = append(def.Tools, n)
			}
		}
	case []any:
		for _, n := range t {
			def.Tools = append(def.Tools, strings.TrimSpace(fmt.Sprint(n)))
		}
	}
	return def, nil
}

func (s *Set) Get(name string) (Definition, bool) {
	if s == nil {
		return Definition{}, false
	}
	d, ok := s.byName[name]
	return d, ok
}

// List returns definitions sorted by name.
func (s *Set) List() []Definition {
	if s == nil {
		return nil
	}
	out := make([]Definition, 0, len(s.byName))
	for _, d := range s.byName {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// toolAllowed reports whether a definition's tool list permits name.
// Matching is case-insensitive so Claude Code names (Read, Grep) work;
// "mcp__server" allows all of a server's tools; "*" allows everything.
func (d Definition) toolAllowed(name string) bool {
	if d.Tools == nil {
		return true
	}
	// Claude Code names tools like MultiEdit; multi_edit is a way of
	// editing, so a definition allowing edit allows it too.
	flat := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, "_", "")) }
	for _, t := range d.Tools {
		switch {
		case t == "*", flat(t) == flat(name):
			return true
		case name == tools.MultiEditToolName && strings.EqualFold(t, "edit"):
			return true
		case strings.HasPrefix(t, "mcp__") && !strings.Contains(t[len("mcp__"):], "__") && strings.HasPrefix(name, t+"__"):
			return true
		}
	}
	return false
}
