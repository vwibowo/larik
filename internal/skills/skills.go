// Package skills discovers Agent Skills (folders with a SKILL.md) and
// exposes them with progressive disclosure: only each skill's name and
// description go into the system prompt; the body is loaded on demand.
package skills

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const (
	maxDescription = 1024
	maxIndexed     = 200 // skills listed in the system prompt
)

type Skill struct {
	Name        string
	Description string
	Path        string // SKILL.md
	Dir         string
	Scope       string // "user" or "project"
	// ModelInvocable skills appear in the system prompt index.
	ModelInvocable bool
	// UserInvocable skills can be run as /name.
	UserInvocable bool
}

type frontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
	UserInvocable          *bool  `yaml:"user-invocable"`
}

// Set is the discovered skills, keyed by name. Reload rescans the same
// roots in place, so every session sharing the set sees new skills.
type Set struct {
	mu       sync.RWMutex
	roots    []Root
	byName   map[string]Skill
	Shadowed []Skill  // lower-precedence duplicates
	Warnings []string // unreadable or malformed skills
}

// Root is a directory whose subdirectories are skills.
type Root struct {
	Dir   string
	Scope string
}

// Roots lists skill directories in increasing precedence: user dirs, then
// project dirs from the repo root down to cwd. Within a scope, .larik
// beats .claude and .agents, so Larik-specific skills can override shared ones.
func Roots(home, configDir, cwd, repoRoot string) []Root {
	roots := []Root{
		{filepath.Join(home, ".agents", "skills"), "user"},
		{filepath.Join(home, ".claude", "skills"), "user"},
		{filepath.Join(configDir, "skills"), "user"},
	}
	var dirs []string
	for d := cwd; ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if repoRoot == "" || d == repoRoot || d == filepath.Dir(d) {
			break
		}
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		for _, sub := range []string{".agents", ".claude", ".larik"} {
			roots = append(roots, Root{filepath.Join(dirs[i], sub, "skills"), "project"})
		}
	}
	return roots
}

// Discover loads skills from roots; later roots override earlier ones.
func Discover(roots []Root) *Set {
	s := &Set{roots: roots, byName: map[string]Skill{}}
	for _, root := range roots {
		entries, err := os.ReadDir(root.Dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			dir := filepath.Join(root.Dir, e.Name())
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() { // follows symlinks
				continue
			}
			path := filepath.Join(dir, "SKILL.md")
			if _, err := os.Stat(path); err != nil {
				continue
			}
			sk, err := parse(path, e.Name(), root.Scope)
			if err != nil {
				s.Warnings = append(s.Warnings, err.Error())
				continue
			}
			if prev, ok := s.byName[sk.Name]; ok && !sameFile(prev.Path, sk.Path) {
				s.Shadowed = append(s.Shadowed, prev)
			}
			s.byName[sk.Name] = sk
		}
	}
	return s
}

// Reload rescans the roots the set was discovered from.
func (s *Set) Reload() {
	if s == nil {
		return
	}
	fresh := Discover(s.roots)
	s.mu.Lock()
	s.byName, s.Shadowed, s.Warnings = fresh.byName, fresh.Shadowed, fresh.Warnings
	s.mu.Unlock()
}

func sameFile(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

var nameRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func parse(path, dirName, scope string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	fm, _, err := split(data)
	if err != nil {
		return Skill{}, fmt.Errorf("%s: %v", path, err)
	}
	var meta frontmatter
	if err := yaml.Unmarshal(fm, &meta); err != nil {
		return Skill{}, fmt.Errorf("%s: invalid frontmatter: %v", path, err)
	}
	name := strings.TrimSpace(meta.Name)
	if name == "" {
		name = dirName
	}
	if !nameRe.MatchString(name) || len(name) > 64 {
		return Skill{}, fmt.Errorf("%s: invalid skill name %q (lowercase letters, digits and hyphens, max 64)", path, name)
	}
	desc := strings.Join(strings.Fields(meta.Description), " ")
	if len(desc) > maxDescription {
		desc = desc[:maxDescription]
	}
	sk := Skill{
		Name:           name,
		Description:    desc,
		Path:           path,
		Dir:            filepath.Dir(path),
		Scope:          scope,
		ModelInvocable: !meta.DisableModelInvocation && desc != "",
		UserInvocable:  meta.UserInvocable == nil || *meta.UserInvocable,
	}
	return sk, nil
}

// split separates YAML frontmatter from the markdown body.
func split(data []byte) (fm, body []byte, err error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // UTF-8 BOM
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(data, []byte("---\n")) {
		return nil, data, fmt.Errorf("missing YAML frontmatter")
	}
	rest := data[4:]
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return nil, nil, fmt.Errorf("unterminated frontmatter")
	}
	fm = rest[:end]
	body = rest[end+4:]
	if i := bytes.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = nil
	}
	return fm, body, nil
}

func (s *Set) Get(name string) (Skill, bool) {
	if s == nil {
		return Skill{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.byName[name]
	return sk, ok
}

// List returns skills sorted by name.
func (s *Set) List() []Skill {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Skill, 0, len(s.byName))
	for _, sk := range s.byName {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Body returns a skill's instructions without frontmatter.
func (s *Set) Body(name string) (Skill, string, error) {
	sk, ok := s.Get(name)
	if !ok {
		return Skill{}, "", fmt.Errorf("no skill named %q", name)
	}
	data, err := os.ReadFile(sk.Path)
	if err != nil {
		return sk, "", err
	}
	_, body, err := split(data)
	if err != nil {
		return sk, "", err
	}
	return sk, strings.TrimSpace(string(body)), nil
}

// Index is the system-prompt section listing model-invocable skills.
// It is empty when there are none.
func (s *Set) Index() string {
	var lines []string
	all := s.List()
	for _, sk := range all {
		if !sk.ModelInvocable {
			continue
		}
		if len(lines) == maxIndexed {
			lines = append(lines, fmt.Sprintf("- (%d more skills not listed)", len(all)-maxIndexed))
			break
		}
		lines = append(lines, "- "+sk.Name+": "+sk.Description)
	}
	if len(lines) == 0 {
		return ""
	}
	return "<skills>\nSkills are folders of instructions, scripts and resources for specialized tasks. " +
		"When a task matches a skill's description, call the skill tool with its name before starting and follow what it says. " +
		"Relative paths inside a skill are relative to its base directory; read bundled files only when needed.\n\n" +
		strings.Join(lines, "\n") + "\n</skills>"
}

// Render formats a loaded skill for the model.
func Render(sk Skill, body, args string) string {
	if strings.Contains(body, "$ARGUMENTS") {
		body = strings.ReplaceAll(body, "$ARGUMENTS", args)
		args = ""
	}
	out := fmt.Sprintf("<skill name=%q base_dir=%q>\n%s\n</skill>", sk.Name, sk.Dir, body)
	if strings.TrimSpace(args) != "" {
		out += "\n\nARGUMENTS: " + strings.TrimSpace(args)
	}
	return out
}

// Expand turns a "/skill-name args" prompt into the skill's instructions.
// It reports false when prompt is not a user-invocable skill command.
func (s *Set) Expand(prompt string) (string, bool) {
	if s == nil || !strings.HasPrefix(prompt, "/") {
		return prompt, false
	}
	name, args, _ := strings.Cut(strings.TrimPrefix(prompt, "/"), " ")
	name = strings.TrimSpace(name)
	sk, ok := s.Get(name)
	if !ok || !sk.UserInvocable {
		return prompt, false
	}
	sk, body, err := s.Body(name)
	if err != nil {
		return prompt, false
	}
	return "The user invoked the /" + name + " skill.\n\n" + Render(sk, body, args), true
}

// SplitFrontmatter separates YAML frontmatter from a markdown body.
func SplitFrontmatter(data []byte) (fm, body []byte, err error) { return split(data) }
