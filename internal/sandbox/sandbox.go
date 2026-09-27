// Package sandbox confines shell commands with the operating system's
// sandbox: Seatbelt (sandbox-exec) on macOS, bubblewrap on Linux.
//
// Inside the sandbox commands can read everything, but write only to the
// project, temp directories and common build caches; configuration that
// could escalate privileges (git hooks and config, Larik/Claude settings,
// .mcp.json) stays read-only; and the network is off except localhost.
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// Config comes from settings files under "sandbox".
type Config struct {
	// Enabled defaults to true wherever a sandbox is available.
	Enabled *bool `json:"enabled,omitempty"`
	// Network allows outbound network access (localhost is always allowed).
	Network bool `json:"network,omitempty"`
	// Writable adds paths commands may write to ("~/" is expanded).
	Writable []string `json:"writable,omitempty"`
}

type Sandbox struct {
	kind      string // "seatbelt" or "bubblewrap"
	root      string
	writable  []string
	protected []string
	network   bool
	profile   string // Seatbelt profile
}

// protectedNames are project paths that must stay read-only even though
// the project is writable: changing them would let a sandboxed command
// run code outside the sandbox later (git hooks, hooksPath, Larik hooks
// and MCP servers).
var protectedNames = []string{".git/hooks", ".git/config", ".larik", ".claude", ".mcp.json"}

// New returns the sandbox for this machine, or nil when disabled or
// unavailable; warning explains an unavailable sandbox.
func New(cfg Config, root, home string) (sb *Sandbox, warning string) {
	if cfg.Enabled != nil && !*cfg.Enabled {
		return nil, ""
	}
	s := &Sandbox{root: real(root), network: cfg.Network}
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			return nil, "sandbox-exec not found; bash commands run unsandboxed and ask for approval"
		}
		s.kind = "seatbelt"
	case "linux":
		if _, err := exec.LookPath("bwrap"); err != nil {
			return nil, "bubblewrap (bwrap) is not installed; bash commands run unsandboxed and ask for approval. Install it (e.g. apt install bubblewrap) to enable the sandbox"
		}
		s.kind = "bubblewrap"
	default:
		return nil, "no sandbox available on " + runtime.GOOS + "; bash commands run unsandboxed and ask for approval"
	}

	s.writable = uniq(append(append([]string{s.root}, defaultWritable(home)...), expand(cfg.Writable, home)...))
	for _, name := range protectedNames {
		s.protected = append(s.protected, filepath.Join(s.root, name))
	}
	if s.kind == "seatbelt" {
		s.profile = s.seatbeltProfile()
	}
	return s, ""
}

// ForWorktree derives a sandbox for a git worktree at dir: writable are
// the worktree (instead of the project) and gitDir, the repository's
// shared .git that commits in the worktree write to; its hooks and config
// stay protected, as do the worktree's own protected paths.
func (s *Sandbox) ForWorktree(dir, gitDir string) *Sandbox {
	w := *s
	w.root = real(dir)
	gitDir = real(gitDir)
	w.writable = []string{w.root, gitDir}
	for _, p := range s.writable {
		if p != s.root {
			w.writable = append(w.writable, p)
		}
	}
	w.writable = uniq(w.writable)
	w.protected = []string{filepath.Join(gitDir, "hooks"), filepath.Join(gitDir, "config")}
	for _, name := range protectedNames {
		w.protected = append(w.protected, filepath.Join(w.root, name))
	}
	// In a worktree .git is a file pointing at the repository; keep it.
	w.protected = append(w.protected, filepath.Join(w.root, ".git"))
	if w.kind == "seatbelt" {
		w.profile = w.seatbeltProfile()
	}
	return &w
}

func defaultWritable(home string) []string {
	paths := []string{"/tmp", os.TempDir()}
	if runtime.GOOS == "darwin" {
		// Per-user temp root (/var/folders/xx/yyy): holds T/ (TMPDIR) and
		// C/ (caches used by clang, swift and friends).
		paths = append(paths, filepath.Dir(real(os.TempDir())), filepath.Join(home, "Library", "Caches"))
	}
	paths = append(paths,
		filepath.Join(home, ".cache"),
		filepath.Join(home, "go", "pkg", "mod"),
		filepath.Join(home, ".npm"),
		filepath.Join(home, ".cargo", "registry"),
		filepath.Join(home, ".cargo", "git"),
	)
	if v := os.Getenv("GOMODCACHE"); v != "" {
		paths = append(paths, v)
	}
	if v := os.Getenv("GOCACHE"); v != "" {
		paths = append(paths, v)
	}
	return paths
}

func expand(paths []string, home string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "~" || strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
		if filepath.IsAbs(p) {
			out = append(out, p)
		}
	}
	return out
}

// real resolves symlinks (Seatbelt matches resolved paths: /tmp is
// /private/tmp on macOS); missing paths are just cleaned.
func real(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

func uniq(paths []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range paths {
		p = real(p)
		if p == "" || p == "/" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func (s *Sandbox) Kind() string { return s.kind }

// Command returns an exec.Cmd that runs script with bash in dir, confined.
func (s *Sandbox) Command(script, dir string) *exec.Cmd {
	var cmd *exec.Cmd
	if s.kind == "seatbelt" {
		cmd = exec.Command("sandbox-exec", "-p", s.profile, "bash", "-c", script)
	} else {
		cmd = exec.Command("bwrap", s.bwrapArgs(script, dir)...)
	}
	cmd.Dir = dir
	return cmd
}

// Summary describes the sandbox for the system prompt and UI.
func (s *Sandbox) Summary() string {
	net := "off (localhost only)"
	if s.network {
		net = "on"
	}
	return fmt.Sprintf("bash commands run in a %s sandbox: writes allowed only in %s, temp directories and build caches; network %s; "+
		".git/hooks, .git/config, .larik, .claude and .mcp.json are read-only", s.kind, s.root, net)
}

// Writable lists paths commands may write to.
func (s *Sandbox) Writable() []string { return s.writable }

// sbQuote quotes a string for the Seatbelt profile language.
func sbQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (s *Sandbox) seatbeltProfile() string {
	var b strings.Builder
	b.WriteString(`(version 1)
(deny default)
(allow process-exec process-fork)
(allow signal (target same-sandbox))
(allow process-info* (target same-sandbox))
(allow sysctl-read)
(allow user-preference-read)
(allow file-read*)
(allow pseudo-tty)
(allow ipc-posix-sem ipc-posix-shm)
(allow iokit-open (iokit-registry-entry-class "RootDomainUserClient"))
; Only the system services ordinary CLI tools need. LaunchServices and
; Apple Events stay blocked, so "open" and osascript can't escape.
(allow mach-lookup
  (global-name "com.apple.system.opendirectoryd.libinfo")
  (global-name "com.apple.bsd.dirhelper")
  (global-name "com.apple.system.logger")
  (global-name "com.apple.system.notification_center")
  (global-name "com.apple.diagnosticd")
  (global-name "com.apple.logd")
  (global-name "com.apple.SecurityServer")
  (global-name "com.apple.trustd.agent"))
(allow file-write* file-ioctl
  (literal "/dev/null") (literal "/dev/zero") (literal "/dev/dtracehelper") (literal "/dev/ptmx")
  (regex #"^/dev/tty") (regex #"^/dev/fd/"))
`)
	b.WriteString("(allow file-write*")
	for _, p := range s.writable {
		b.WriteString("\n  (subpath " + sbQuote(p) + ")")
	}
	b.WriteString(")\n")
	// Later rules win in Seatbelt, so these override the project allow.
	b.WriteString("(deny file-write*")
	for _, p := range s.protected {
		b.WriteString("\n  (subpath " + sbQuote(p) + ")")
	}
	b.WriteString(")\n")
	b.WriteString(`(allow network-bind network-inbound (local ip "localhost:*"))
(allow network-outbound (remote ip "localhost:*"))
`)
	if s.network {
		b.WriteString(`(allow network*)
(allow system-socket)
(allow mach-lookup (global-name "com.apple.dnssd.service") (global-name "com.apple.nsurlsessiond") (global-name "com.apple.networkd"))
`)
	}
	return b.String()
}

func (s *Sandbox) bwrapArgs(script, dir string) []string {
	args := []string{"--die-with-parent", "--unshare-pid", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
	for _, p := range s.writable {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--bind", p, p)
		}
	}
	for _, p := range s.protected {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--ro-bind", p, p)
		}
	}
	if !s.network {
		args = append(args, "--unshare-net") // leaves only loopback
	}
	return append(args, "--chdir", dir, "--", "bash", "-c", script)
}
