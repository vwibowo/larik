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
	// Command marks a Claude Code-style custom command: a single
	// commands/<name>.md file rather than a <name>/SKILL.md folder.
	Command bool
	// ArgumentHint describes a command's arguments, e.g. "[issue-number]".
	ArgumentHint string
	// Builtin marks a command that ships with Larik (see builtin.go); data
	// is its file, which isn't on disk.
	Builtin bool
	data    []byte
}

type frontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation"`
	UserInvocable          *bool  `yaml:"user-invocable"`
	ArgumentHint           string `yaml:"argument-hint"`
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

// Root is a directory whose subdirectories are skills, or with Commands,
// whose .md files (in any subdirectory) are commands.
type Root struct {
	Dir      string
	Scope    string
	Commands bool
}

// Roots lists skill and command directories in increasing precedence:
// commands first, so a skill wins over a command of the same name (as in
// Claude Code); then, for each, user dirs, then project dirs from the repo
// root down to cwd. Within a scope, .larik beats .claude and .agents, so
// Larik-specific skills can override shared ones.
func Roots(home, configDir, cwd, repoRoot string) []Root {
	var dirs []string
	for d := cwd; ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if repoRoot == "" || d == repoRoot || d == filepath.Dir(d) {
			break
		}
	}
	roots := []Root{
		{filepath.Join(home, ".claude", "commands"), "user", true},
		{filepath.Join(configDir, "commands"), "user", true},
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		for _, sub := range []string{".claude", ".larik"} {
			roots = append(roots, Root{filepath.Join(dirs[i], sub, "commands"), "project", true})
		}
	}
	roots = append(roots,
		Root{filepath.Join(home, ".agents", "skills"), "user", false},
		Root{filepath.Join(home, ".claude", "skills"), "user", false},
		Root{filepath.Join(configDir, "skills"), "user", false},
	)
	for i := len(dirs) - 1; i >= 0; i-- {
		for _, sub := range []string{".agents", ".claude", ".larik"} {
			roots = append(roots, Root{filepath.Join(dirs[i], sub, "skills"), "project", false})
		}
	}
	return roots
}

// Discover loads skills from roots; later roots override earlier ones.
func Discover(roots []Root) *Set {
	s := &Set{roots: roots, byName: map[string]Skill{}}
	add := func(sk Skill) {
		// Replacing a built-in command with your own is not a conflict.
		if prev, ok := s.byName[sk.Name]; ok && !prev.Builtin && !sameFile(prev.Path, sk.Path) {
			s.Shadowed = append(s.Shadowed, prev)
		}
		s.byName[sk.Name] = sk
	}
	// Built-in commands come first, so anything found on disk wins.
	for _, sk := range builtins() {
		s.byName[sk.Name] = sk
	}
	for _, root := range roots {
		if root.Commands {
			for _, path := range commandFiles(root.Dir) {
				sk, err := parseCommand(path, root.Scope)
				if err != nil {
					s.Warnings = append(s.Warnings, err.Error())
					continue
				}
				add(sk)
			}
			continue
		}
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
			add(sk)
		}
	}
	return s
}

// commandFiles lists the .md files under dir, sorted, following
// Claude Code: a subdirectory only groups commands (frontend/test.md is
// /test).
func commandFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && path != dir && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), ".md") {
			out = append(out, path)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

var hintRe = regexp.MustCompile(`(?m)^argument-hint:[ \t]*(.*)$`)

// unmarshalFrontmatter parses YAML frontmatter. Claude Code writes
// argument hints like "argument-hint: [pr] [priority]", which isn't valid
// YAML, so that line is taken as it stands.
func unmarshalFrontmatter(fm []byte, meta *frontmatter) error {
	if m := hintRe.FindSubmatchIndex(fm); m != nil {
		meta.ArgumentHint = strings.Trim(strings.TrimSpace(string(fm[m[2]:m[3]])), `"'`)
		fm = append(append([]byte(nil), fm[:m[0]]...), fm[m[1]:]...)
	}
	return yaml.Unmarshal(fm, meta)
}

var commandNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// parseCommand reads a custom command file. Frontmatter is optional; a
// command without a description uses its first line.
func parseCommand(path, scope string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, err
	}
	return parseCommandData(path, data, scope)
}

func parseCommandData(path string, data []byte, scope string) (Skill, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !commandNameRe.MatchString(name) || len(name) > 64 {
		return Skill{}, fmt.Errorf("%s: invalid command name %q (letters, digits, - and _, max 64)", path, name)
	}
	var meta frontmatter
	body := data
	if fm, b, err := split(data); err == nil {
		if err := unmarshalFrontmatter(fm, &meta); err != nil {
			return Skill{}, fmt.Errorf("%s: invalid frontmatter: %v", path, err)
		}
		body = b
	}
	desc := strings.Join(strings.Fields(meta.Description), " ")
	explicit := desc != ""
	if !explicit {
		desc = firstLine(string(body))
	}
	if r := []rune(desc); len(r) > maxDescription {
		desc = string(r[:maxDescription])
	}
	return Skill{
		Name:        name,
		Description: desc,
		Path:        path,
		Dir:         filepath.Dir(path),
		Scope:       scope,
		// As in Claude Code, the model may run a command only when its
		// author described it.
		ModelInvocable: explicit && !meta.DisableModelInvocation,
		UserInvocable:  meta.UserInvocable == nil || *meta.UserInvocable,
		Command:        true,
		ArgumentHint:   strings.TrimSpace(meta.ArgumentHint),
	}, nil
}

// firstLine is a body's first non-empty line, without heading marks.
func firstLine(body string) string {
	for _, l := range strings.Split(body, "\n") {
		if l = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(l), "#")); l != "" {
			if r := []rune(l); len(r) > 100 {
				l = string(r[:99]) + "…"
			}
			return l
		}
	}
	return ""
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
	if err := unmarshalFrontmatter(fm, &meta); err != nil {
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
	if r := []rune(desc); len(r) > maxDescription {
		desc = string(r[:maxDescription])
	}
	sk := Skill{
		Name:           name,
		Description:    desc,
		Path:           path,
		Dir:            filepath.Dir(path),
		Scope:          scope,
		ModelInvocable: !meta.DisableModelInvocation && desc != "",
		UserInvocable:  meta.UserInvocable == nil || *meta.UserInvocable,
		ArgumentHint:   strings.TrimSpace(meta.ArgumentHint),
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
	data := sk.data
	if !sk.Builtin {
		var err error
		if data, err = os.ReadFile(sk.Path); err != nil {
			return sk, "", err
		}
	}
	_, body, err := split(data)
	if err != nil {
		if !sk.Command { // a command's frontmatter is optional
			return sk, "", err
		}
		body = bytes.TrimPrefix(bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")), []byte("\xef\xbb\xbf"))
	}
	return sk, strings.TrimSpace(string(body)), nil
}

// Index budgets: each description is clipped, and past indexBudget the
// remaining skills are listed by name only. The index is in every
// request's system prompt.
const (
	maxIndexDesc = 250
	indexBudget  = 8000
)

// Index is the system-prompt section listing model-invocable skills.
// It is empty when there are none.
func (s *Set) Index() string {
	var invocable []Skill
	for _, sk := range s.List() {
		if sk.ModelInvocable {
			invocable = append(invocable, sk)
		}
	}
	if len(invocable) == 0 {
		return ""
	}
	var b strings.Builder
	var namesOnly []string
	for i, sk := range invocable {
		if i == maxIndexed {
			fmt.Fprintf(&b, "- (%d more skills not listed)\n", len(invocable)-maxIndexed)
			break
		}
		line := "- " + sk.Name + ": " + clip(sk.Description, maxIndexDesc) + "\n"
		if len(namesOnly) > 0 || b.Len()+len(line) > indexBudget {
			namesOnly = append(namesOnly, sk.Name)
			continue
		}
		b.WriteString(line)
	}
	if len(namesOnly) > 0 {
		b.WriteString("- More skills (descriptions load with the skill): " + strings.Join(namesOnly, ", ") + "\n")
	}
	return "<skills>\nSkills are folders of instructions, scripts and resources for specialized tasks. " +
		"When a task matches a skill's description, call the skill tool with its name before starting and follow what it says. " +
		"Relative paths inside a skill are relative to its base directory; read bundled files only when needed.\n\n" +
		b.String() + "</skills>"
}

// clip shortens s to n runes on one line.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

var positional = regexp.MustCompile(`\$([1-9])`)

// Render formats a loaded skill for the model. $ARGUMENTS in the body
// becomes the arguments, and $1…$9 the arguments split on spaces; when
// the body uses neither, the arguments follow it.
func Render(sk Skill, body, args string) string {
	used := false
	if strings.Contains(body, "$ARGUMENTS") {
		body = strings.ReplaceAll(body, "$ARGUMENTS", args)
		used = true
	}
	if positional.MatchString(body) {
		fields := strings.Fields(args)
		body = positional.ReplaceAllStringFunc(body, func(m string) string {
			if i := int(m[1] - '1'); i < len(fields) {
				return fields[i]
			}
			return ""
		})
		used = true
	}
	if used {
		args = ""
	}
	tag := "skill"
	if sk.Command {
		tag = "command"
	}
	out := fmt.Sprintf("<%s name=%q base_dir=%q>\n%s\n</%s>", tag, sk.Name, sk.Dir, body, tag)
	if sk.Builtin { // no directory to resolve paths against
		out = fmt.Sprintf("<%s name=%q>\n%s\n</%s>", tag, sk.Name, body, tag)
	}
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
	kind := "skill"
	if sk.Command {
		kind = "command"
	}
	return "The user invoked the /" + name + " " + kind + ".\n\n" + Render(sk, body, args), true
}

// SplitFrontmatter separates YAML frontmatter from a markdown body.
func SplitFrontmatter(data []byte) (fm, body []byte, err error) { return split(data) }
