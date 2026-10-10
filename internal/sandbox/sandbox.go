// Package sandbox confines shell commands with the operating system's
// sandbox: Seatbelt (sandbox-exec) on macOS, bubblewrap on Linux.
//
// Inside the sandbox commands can read everything, but write only to the
// project, temp directories and common build caches; configuration that
// could escalate privileges (git hooks and config, Larik/Claude settings,
// .mcp.json) stays read-only; and the network is off except localhost,
// or reaches only allowed domains through a proxy (see proxy.go).
package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"larik/internal/pathpolicy"
)

// Config comes from settings files under "sandbox".
type Config struct {
	// Enabled defaults to true wherever a sandbox is available.
	Enabled *bool `json:"enabled,omitempty"`
	// Network allows outbound network access (localhost is always allowed).
	Network bool `json:"network,omitempty"`
	// Writable adds paths commands may write to ("~/" is expanded).
	Writable []string `json:"writable,omitempty"`
	// AllowedDomains, with Network off, lets commands reach these domains
	// (and their subdomains) through a proxy Larik runs; nothing else.
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	// EnvPassthrough names environment variables that commands may see even
	// though they look like credentials (personal files only: it widens
	// access).
	EnvPassthrough []string `json:"env_passthrough,omitempty"`
	// SecretEnv and SecretPaths are filled in by the app, not read from
	// settings: the credential variables and files that sandboxed commands
	// must not see. Any variable ending in _API_KEY, _AUTH_TOKEN or
	// _ACCESS_TOKEN is hidden as well, so providers Larik doesn't know
	// about are covered.
	SecretEnv   []string `json:"-"`
	SecretPaths []string `json:"-"`
}

type Sandbox struct {
	kind      string // "seatbelt" or "bubblewrap"
	root      string
	writable  []string
	protected []string
	// pinned directories stay writable inside but can't be renamed,
	// removed or replaced: the project's .git, which git trusts to be
	// where the repository is.
	pinned  []string
	network bool
	profile string // Seatbelt profile
	// tmpDir is this sandbox's own private scratch directory: the only
	// place outside root, gitDir and the persistent build caches that a
	// sandboxed command may write to. It stands in for the system temp
	// directory, which sandboxed bash never gets write access to (see
	// New), and is removed by Close. A worktree-derived Sandbox
	// (ForWorktree) shares its parent's tmpDir rather than getting its
	// own, so only the top-level Sandbox should have Close called on it.
	tmpDir string
	// proxy, when domains are allowed, is shared with worktree sandboxes;
	// exe is the larik binary, which runs the network bridge on Linux.
	proxy *proxy
	exe   string
	// home is the user's home directory, which confined programs can't
	// read (see Confine).
	home string
	// noConfine is set when Confine's stricter sandbox can't start here
	// (see New); Confine then leaves commands unconfined.
	noConfine bool
	// holders keeps missing protected paths from being created under
	// bubblewrap (see holders.go); nil with Seatbelt, which denies by path.
	holders *holders
	// secretEnv, passthrough and secretPaths keep credentials out of
	// sandboxed commands (see scrubEnv and the profile builders).
	secretEnv   map[string]bool
	passthrough map[string]bool
	secretPaths []string
}

// protectedNames are project paths that must stay read-only even though
// the project is writable: changing them would let a sandboxed command
// run code outside the sandbox later (git hooks, hooksPath, Larik hooks
// and MCP servers).
var protectedNames = []string{".larik", ".claude", ".mcp.json"}

// gitProtected are the git dir entries sandboxed commands can't change;
// see pathpolicy.GitProtected.
var gitProtected = pathpolicy.GitProtected

// New returns the sandbox for this machine, or nil when disabled or
// unavailable; warning explains an unavailable sandbox.
func New(cfg Config, root, home string) (sb *Sandbox, warning string) {
	if cfg.Enabled != nil && !*cfg.Enabled {
		return nil, ""
	}
	s := &Sandbox{root: real(root), network: cfg.Network, home: real(home)}
	s.secretEnv, s.passthrough = nameSet(cfg.SecretEnv), nameSet(cfg.EnvPassthrough)
	for _, p := range cfg.SecretPaths {
		if filepath.IsAbs(p) {
			s.secretPaths = append(s.secretPaths, real(p))
		}
	}
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

	tmpDir, err := os.MkdirTemp("", "larik-sandbox-")
	if err != nil {
		return nil, "could not create a private temp directory (" + err.Error() + "); bash commands run unsandboxed and ask for approval"
	}
	s.tmpDir = real(tmpDir)
	if s.kind == "bubblewrap" {
		s.holders = newHolders()
	}

	s.writable = uniq(append(append([]string{s.root, s.tmpDir}, defaultWritable(home)...), expand(cfg.Writable, home)...))
	for _, name := range protectedNames {
		s.protected = append(s.protected, filepath.Join(s.root, name))
	}
	gitDir := filepath.Join(s.root, ".git")
	for _, name := range gitProtected {
		s.protected = append(s.protected, filepath.Join(gitDir, name))
	}
	if fi, err := os.Lstat(gitDir); err == nil && fi.IsDir() {
		s.pinned = []string{gitDir}
	} else {
		// A .git file (a worktree or submodule checkout) or none at all:
		// keep it from being created or pointed elsewhere.
		s.protected = append(s.protected, gitDir)
	}
	if len(cfg.AllowedDomains) > 0 && !cfg.Network {
		warning = s.startProxy(cfg.AllowedDomains)
	}
	if s.kind == "seatbelt" {
		s.profile = s.seatbeltProfile()
	}
	// Installed isn't the same as working: inside a container, or under
	// another sandbox, the OS can refuse what the sandbox needs. Then every
	// command would fail, so fall back as if there were no sandbox.
	if err := s.probe(); err != nil {
		s.Close()
		return nil, "the " + s.kind + " sandbox can't start here (" + err.Error() + "); bash commands run unsandboxed and ask for approval" + probeHint(s.kind, err.Error(), inContainer())
	}
	if err := s.probeConfine(); err != nil {
		s.noConfine = true
		warning = strings.TrimPrefix(warning+"; ", "; ") + "Larik's helpers (the run_code script runner) can't be confined here (" + err.Error() + ") and run unconfined"
	}
	return s, warning
}

// startProxy starts the allowlist proxy. On Linux the command reaches it
// through the bridge, which needs the larik binary and a Unix socket.
func (s *Sandbox) startProxy(domains []string) (warning string) {
	sock := ""
	if s.kind == "bubblewrap" {
		exe, err := os.Executable()
		if err != nil {
			return "the sandbox network allowlist is off: " + err.Error()
		}
		s.exe = real(exe)
		sock = filepath.Join(s.tmpDir, "proxy.sock")
	}
	p, err := startProxy(domains, sock)
	if err != nil {
		return "the sandbox network allowlist is off (its proxy didn't start: " + err.Error() + "); the network stays off"
	}
	s.proxy = p
	return ""
}

// NetworkBlocked lists the hosts the allowlist proxy refused since t.
func (s *Sandbox) NetworkBlocked(since time.Time) []string {
	if s == nil || s.proxy == nil {
		return nil
	}
	return s.proxy.blockedSince(since)
}

// Close removes this sandbox's private temp directory. Call it once, on
// the top-level Sandbox from New, when it is no longer needed; a
// worktree-derived Sandbox shares that directory and must not be closed
// on its own.
func (s *Sandbox) Close() error {
	if s == nil || s.tmpDir == "" {
		return nil
	}
	if s.proxy != nil {
		s.proxy.Close()
	}
	if s.holders != nil {
		s.holders.close()
	}
	return os.RemoveAll(s.tmpDir)
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
	w.protected = nil
	for _, name := range gitProtected {
		if name != "worktrees" { // commits in the worktree write there
			w.protected = append(w.protected, filepath.Join(gitDir, name))
		}
	}
	// The worktree's own directory under gitDir/worktrees says where the
	// common git dir is and may hold config; keep those.
	if own := worktreeGitDir(w.root); own != "" {
		w.protected = append(w.protected, filepath.Join(own, "commondir"), filepath.Join(own, "config.worktree"), filepath.Join(own, "gitdir"))
	}
	for _, name := range protectedNames {
		w.protected = append(w.protected, filepath.Join(w.root, name))
	}
	// In a worktree .git is a file pointing at the repository; keep it.
	w.protected = append(w.protected, filepath.Join(w.root, ".git"))
	w.pinned = []string{gitDir}
	if w.kind == "seatbelt" {
		w.profile = w.seatbeltProfile()
	}
	return &w
}

// worktreeGitDir reads the "gitdir: …" line of a worktree's .git file.
func worktreeGitDir(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".git"))
	if err != nil {
		return ""
	}
	p, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok {
		return ""
	}
	p = strings.TrimSpace(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return real(p)
}

// defaultWritable is the persistent, per-user directories a sandboxed
// command may write to. It deliberately excludes the literal /tmp: on
// every OS that's a single directory shared by every program any user on
// the machine runs (Claude Code's own scratch files live there, for
// instance), so granting it broad write access let a sandboxed command
// destroy another program's files with a stray "rm -rf /tmp/..." during
// testing. Each Sandbox instead gets its own private scratch directory
// (New's tmpDir), which Command exposes to the child as
// $TMPDIR/$TMP/$TEMP, for tools that read those.
//
// os.TempDir() itself stays writable on macOS (unlike on Linux, where it
// is usually just another name for /tmp): it's scoped to this OS user,
// not shared machine-wide, and several macOS tools resolve to it via
// confstr(_CS_DARWIN_USER_TEMP_DIR) regardless of $TMPDIR -- notably
// mktemp(1) with no template, which real build and setup scripts use.
func defaultWritable(home string) []string {
	var paths []string
	if runtime.GOOS == "darwin" {
		paths = append(paths, os.TempDir(), filepath.Dir(real(os.TempDir())), filepath.Join(home, "Library", "Caches"))
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
// $TMPDIR, $TMP and $TEMP point at this sandbox's own scratch directory,
// so well-behaved tools (mktemp, go build, npm, …) land somewhere
// writable without needing the real system temp directory.
func (s *Sandbox) Command(script, dir string) *exec.Cmd {
	var cmd *exec.Cmd
	if s.kind == "seatbelt" {
		cmd = exec.Command("sandbox-exec", "-p", s.profile, "bash", "-c", script)
	} else {
		// Placeholders first: bwrapArgs binds the protected paths that exist.
		var held []string
		if s.holders != nil {
			held = s.holders.hold(s.protected, s.writable)
		}
		cmd = exec.Command("bwrap", s.bwrapArgs(script, dir, held...)...)
		if s.holders != nil {
			s.holders.started(cmd, held)
		}
	}
	cmd.Dir = dir
	cmd.Env = s.scrubEnv(tmpEnv(s.tmpDir))
	if s.proxy != nil {
		cmd.Env = proxyEnv(cmd.Env, s.proxy.URL())
	}
	return cmd
}

// proxyEnv points the proxy variables tools read (curl, git, Go, npm, pip,
// cargo; Node's fetch with NODE_USE_ENV_PROXY) at url, for everything but
// localhost, which the sandbox reaches directly.
func proxyEnv(env []string, url string) []string {
	names := []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"}
	out := make([]string, 0, len(env)+len(names)+3)
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "NODE_USE_ENV_PROXY":
			continue
		}
		out = append(out, kv)
	}
	for _, n := range names {
		out = append(out, n+"="+url)
	}
	return append(out, "NO_PROXY=localhost,127.0.0.1,::1", "no_proxy=localhost,127.0.0.1,::1", "NODE_USE_ENV_PROXY=1")
}

// Finished must be called once a command from Command has ended (or
// failed to start), with whether it left a process running: it removes the
// placeholders the Linux sandbox put in the project for the command.
func (s *Sandbox) Finished(cmd *exec.Cmd, leftRunning bool) {
	if s != nil && s.holders != nil {
		s.holders.finished(cmd, leftRunning)
	}
}

// tmpEnv is the current environment with TMPDIR, TMP and TEMP replaced by
// dir, so a script's own $TMPDIR lookup finds a directory the sandbox
// actually allows writing to, however that variable was set for Larik
// itself.
func tmpEnv(dir string) []string {
	env := os.Environ()
	out := make([]string, 0, len(env)+3)
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			switch k {
			case "TMPDIR", "TMP", "TEMP":
				continue
			}
		}
		out = append(out, kv)
	}
	return append(out, "TMPDIR="+dir, "TMP="+dir, "TEMP="+dir)
}

func nameSet(names []string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		if n != "" {
			m[n] = true
		}
	}
	return m
}

// credentialName reports whether an environment variable looks like it
// holds a credential, whatever provider it belongs to.
func credentialName(k string) bool {
	k = strings.ToUpper(k)
	for _, suffix := range []string{"_API_KEY", "_AUTH_TOKEN", "_ACCESS_TOKEN"} {
		if strings.HasSuffix(k, suffix) {
			return true
		}
	}
	return false
}

// scrubEnv removes credentials from env: sandboxed bash reads everything
// and runs without asking, so a prompt-injected `env` would otherwise put
// every provider key into the model's context. Variables the user listed
// in env_passthrough stay.
func (s *Sandbox) scrubEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if !s.passthrough[k] && (s.secretEnv[k] || credentialName(k)) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// Summary describes the sandbox for the system prompt and UI.
func (s *Sandbox) Summary() string {
	net := "off (localhost only)"
	switch {
	case s.network:
		net = "on"
	case s.proxy != nil:
		net = "only to " + strings.Join(s.proxy.allowed, ", ") + " (and their subdomains), through a proxy set in HTTP_PROXY/HTTPS_PROXY; other hosts are refused, and tools that ignore those variables can't connect"
	}
	// The temp directory is named by $TMPDIR rather than its path, which
	// differs per run: the summary is in the system prompt, ahead of
	// sections that should stay cached across sessions.
	return fmt.Sprintf("bash commands run in a %s sandbox: writes allowed only in %s, its own private temp directory ($TMPDIR) and build caches; network %s; "+
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
	for _, p := range s.pinned {
		b.WriteString("\n  (literal " + sbQuote(p) + ")")
	}
	b.WriteString(")\n")
	if len(s.secretPaths) > 0 {
		b.WriteString("(deny file-read*")
		for _, p := range s.secretPaths {
			b.WriteString("\n  (subpath " + sbQuote(p) + ")")
		}
		b.WriteString(")\n")
	}
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

// bwrapArgs builds the bubblewrap command line; held are the placeholders
// standing in for protected paths that don't exist (see holders.go).
func (s *Sandbox) bwrapArgs(script, dir string, held ...string) []string {
	args := []string{"--die-with-parent", "--unshare-pid", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
	for _, p := range s.writable {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--bind", p, p)
		}
	}
	// A mount point can't be renamed or removed, so binding a pinned
	// directory onto itself keeps it in place with its contents writable.
	for _, p := range s.pinned {
		if _, err := os.Stat(p); err == nil {
			args = append(args, "--bind", p, p)
		}
	}
	bound := map[string]bool{}
	for _, p := range append(append([]string(nil), s.protected...), held...) {
		if _, err := os.Stat(p); err == nil && !bound[p] {
			bound[p] = true
			args = append(args, "--ro-bind", p, p)
		}
	}
	// Credentials stay unreadable: a file is replaced by an empty one, a
	// directory by an empty tmpfs.
	for _, p := range s.secretPaths {
		if fi, err := os.Stat(p); err == nil {
			if fi.IsDir() {
				args = append(args, "--tmpfs", p)
			} else {
				args = append(args, "--ro-bind", "/dev/null", p)
			}
		}
	}
	if !s.network {
		args = append(args, "--unshare-net") // leaves only loopback
	}
	args = append(args, "--chdir", dir, "--")
	if s.proxy != nil {
		// The bridge brings the proxy into the network namespace.
		args = append(args, s.exe, BridgeCommand, filepath.Join(s.tmpDir, "proxy.sock"), s.proxy.Port(), "--")
	}
	return append(args, "bash", "-c", script)
}
