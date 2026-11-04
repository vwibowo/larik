// Package integration installs Larik Desktop skills and status hooks into
// coding agents' personal configuration, leaving project files alone.
package integration

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed skills/*/SKILL.md
var skillFiles embed.FS

var skillNames = []string{"larik-desktop", "larik-desktop-browser"}

const managedSkill = "<!-- Managed by Larik Desktop."
const managedPlugin = "// Managed by Larik Desktop."

// Agents returns the supported hook adapters in Settings order.
func Agents() []string { return []string{"claude", "codex", "opencode", "gemini"} }

func Label(agent string) string {
	switch agent {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex CLI"
	case "opencode":
		return "OpenCode"
	case "gemini":
		return "Gemini CLI"
	}
	return agent
}

func skillDir(home, name string) string   { return filepath.Join(home, ".agents", "skills", name) }
func claudeLink(home, name string) string { return filepath.Join(home, ".claude", "skills", name) }

// SkillsPath is the shared global skill location shown in Settings.
func SkillsPath(home string) string { return filepath.Join(home, ".agents", "skills") }

func SkillsInstalled(home string) bool {
	for _, name := range skillNames {
		data, err := os.ReadFile(filepath.Join(skillDir(home, name), "SKILL.md"))
		if err != nil || !strings.Contains(string(data), managedSkill) {
			return false
		}
		link, err := os.Readlink(claudeLink(home, name))
		if err != nil || link != skillDir(home, name) {
			return false
		}
	}
	return true
}

// InstallSkills preflights every destination before writing any skill.
func InstallSkills(home string) error {
	for _, name := range skillNames {
		dir := skillDir(home, name)
		if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
			return fmt.Errorf("%s already exists and is not a directory", dir)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		path := filepath.Join(dir, "SKILL.md")
		if err := regularOrAbsent(path); err != nil {
			return err
		}
		if data, err := os.ReadFile(path); err == nil && !strings.Contains(string(data), managedSkill) {
			return fmt.Errorf("%s already contains another skill", path)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		link := claudeLink(home, name)
		if _, err := os.Lstat(link); err == nil {
			target, err := os.Readlink(link)
			if err != nil || target != dir {
				return fmt.Errorf("%s already exists and is not Larik's link", link)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	for _, name := range skillNames {
		dir := skillDir(home, name)
		data, _ := skillFiles.ReadFile("skills/" + name + "/SKILL.md")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := writeAtomic(filepath.Join(dir, "SKILL.md"), data, 0o644); err != nil {
			return err
		}
		link := claudeLink(home, name)
		if _, err := os.Lstat(link); errors.Is(err, fs.ErrNotExist) {
			if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(dir, link); err != nil {
				return err
			}
		}
	}
	return nil
}

func RemoveSkills(home string) error {
	for _, name := range skillNames {
		if info, err := os.Lstat(skillDir(home, name)); err == nil && !info.IsDir() {
			return fmt.Errorf("%s is not a directory", skillDir(home, name))
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		link := claudeLink(home, name)
		if target, err := os.Readlink(link); err == nil && target == skillDir(home, name) {
			if err := os.Remove(link); err != nil {
				return err
			}
		}
		path := filepath.Join(skillDir(home, name), "SKILL.md")
		if err := regularOrAbsent(path); err != nil {
			return err
		}
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), managedSkill) {
			if err := os.Remove(path); err != nil {
				return err
			}
			_ = os.Remove(skillDir(home, name)) // leave any other files in place
		}
	}
	return nil
}

func HookPath(home, agent string) string {
	switch agent {
	case "claude":
		return filepath.Join(home, ".claude", "settings.json")
	case "codex":
		if dir := os.Getenv("CODEX_HOME"); dir != "" {
			return filepath.Join(dir, "hooks.json")
		}
		return filepath.Join(home, ".codex", "hooks.json")
	case "gemini":
		return filepath.Join(home, ".gemini", "settings.json")
	case "opencode":
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			base = filepath.Join(home, ".config")
		}
		return filepath.Join(base, "opencode", "plugins", "larik-desktop.js")
	}
	return ""
}

func HookInstalled(home, agent string) (bool, error) {
	path := HookPath(home, agent)
	if path == "" {
		return false, fmt.Errorf("unknown agent %q", agent)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if agent == "opencode" {
		return strings.HasPrefix(string(data), managedPlugin), nil
	}
	_, hooks, err := parseConfig(data)
	if err != nil {
		return false, err
	}
	for _, groups := range hooks {
		for _, group := range groups {
			if hasManagedCommand(group, agent) {
				return true, nil
			}
		}
	}
	return false, nil
}

func InstallHook(home, bin, agent string) error {
	path := HookPath(home, agent)
	if path == "" {
		return fmt.Errorf("unknown agent %q", agent)
	}
	if err := regularOrAbsent(path); err != nil {
		return err
	}
	if !filepath.IsAbs(bin) {
		return errors.New("the Larik executable path must be absolute")
	}
	if agent == "opencode" {
		if old, err := os.ReadFile(path); err == nil && !strings.HasPrefix(string(old), managedPlugin) {
			return fmt.Errorf("%s already contains another plugin", path)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return writeAtomic(path, []byte(openCodePlugin(bin)), 0o644)
	}
	data, err := readConfig(path)
	if err != nil {
		return err
	}
	root, hooks, err := parseConfig(data)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	removeManaged(hooks, agent)
	for _, event := range hookEvents(agent) {
		command := shellQuote(bin) + " desktop hook " + agent + " " + event
		handler := map[string]any{"type": "command", "command": command, "timeout": 5}
		if agent == "gemini" {
			handler["name"] = "larik-desktop-status"
			handler["timeout"] = 5000 // Gemini measures hook timeouts in milliseconds.
		}
		group, _ := json.Marshal(map[string]any{"hooks": []any{handler}})
		hooks[event] = append(hooks[event], group)
	}
	return saveConfig(path, data, root, hooks)
}

func RemoveHook(home, agent string) error {
	path := HookPath(home, agent)
	if path == "" {
		return fmt.Errorf("unknown agent %q", agent)
	}
	if err := regularOrAbsent(path); err != nil {
		return err
	}
	if agent == "opencode" {
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(data), managedPlugin) {
			return fmt.Errorf("%s is not Larik's plugin", path)
		}
		return os.Remove(path)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	root, hooks, err := parseConfig(data)
	if err != nil {
		return err
	}
	if !removeManaged(hooks, agent) {
		return nil
	}
	return saveConfig(path, data, root, hooks)
}

func hookEvents(agent string) []string {
	switch agent {
	case "claude":
		return []string{"UserPromptSubmit", "PermissionRequest", "Notification", "PostToolUse", "Stop", "StopFailure"}
	case "codex":
		return []string{"UserPromptSubmit", "PermissionRequest", "PostToolUse", "Stop", "Interrupt"}
	case "gemini":
		return []string{"BeforeAgent", "Notification", "AfterTool", "AfterAgent"}
	}
	return nil
}

func openCodePlugin(bin string) string {
	quoted, _ := json.Marshal(bin)
	return managedPlugin + "\nimport { spawn } from 'node:child_process'\n\nexport const LarikDesktopStatus = async () => ({\n  event: async ({ event }) => {\n    if (!process.env.LARIK_DESKTOP_TAB) return\n    const child = spawn(" + string(quoted) + ", ['desktop', 'hook', 'opencode', event.type], { stdio: ['pipe', 'ignore', 'ignore'] })\n    child.on('error', () => {})\n    child.stdin.on('error', () => {})\n    child.stdin.end(JSON.stringify(event))\n  },\n})\n"
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func readConfig(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []byte("{}"), nil
	}
	return data, err
}

func parseConfig(data []byte) (map[string]json.RawMessage, map[string][]json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil || root == nil {
		return nil, nil, errors.New("settings must be a JSON object")
	}
	hooks := map[string][]json.RawMessage{}
	if raw := root["hooks"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &hooks); err != nil || hooks == nil {
			return nil, nil, errors.New("hooks must be an object of event arrays")
		}
	}
	return root, hooks, nil
}

func hasManagedCommand(group json.RawMessage, agent string) bool {
	var g struct {
		Hooks []struct {
			Command string `json:"command"`
		} `json:"hooks"`
	}
	if json.Unmarshal(group, &g) != nil {
		return false
	}
	for _, hook := range g.Hooks {
		if strings.Contains(hook.Command, " desktop hook "+agent+" ") {
			return true
		}
	}
	return false
}

// removeManaged removes only Larik's handlers, retaining unrelated handlers
// even if they share a matcher group.
func removeManaged(hooks map[string][]json.RawMessage, agent string) bool {
	changed := false
	for event, groups := range hooks {
		var kept []json.RawMessage
		for _, raw := range groups {
			if !hasManagedCommand(raw, agent) {
				kept = append(kept, raw)
				continue
			}
			var group map[string]json.RawMessage
			if json.Unmarshal(raw, &group) != nil {
				kept = append(kept, raw)
				continue
			}
			var commands []json.RawMessage
			if json.Unmarshal(group["hooks"], &commands) != nil {
				kept = append(kept, raw)
				continue
			}
			var remaining []json.RawMessage
			for _, command := range commands {
				var item struct {
					Command string `json:"command"`
				}
				_ = json.Unmarshal(command, &item)
				if strings.Contains(item.Command, " desktop hook "+agent+" ") {
					changed = true
				} else {
					remaining = append(remaining, command)
				}
			}
			if len(remaining) > 0 {
				group["hooks"], _ = json.Marshal(remaining)
				updated, _ := json.Marshal(group)
				kept = append(kept, updated)
			}
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	return changed
}

func saveConfig(path string, old []byte, root map[string]json.RawMessage, hooks map[string][]json.RawMessage) error {
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		root["hooks"], _ = json.Marshal(hooks)
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if string(data) == string(old) {
		return nil
	}
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		backup := fmt.Sprintf("%s.bak.%d", path, time.Now().UnixNano())
		if err := os.WriteFile(backup, old, 0o600); err != nil {
			return err
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return writeAtomic(path, data, 0o600)
}

func writeAtomic(path string, data []byte, mode fs.FileMode) error {
	if old, err := os.ReadFile(path); err == nil && string(old) == string(data) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".larik-integration-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(mode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

func regularOrAbsent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s already exists and is not a regular file", path)
	}
	return nil
}
