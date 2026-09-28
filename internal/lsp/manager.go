// Package lsp runs language servers so the agent gets compiler feedback
// after edits and can navigate code (definitions, references, symbols).
package lsp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ServerConfig describes a language server. JSON keys match settings files.
type ServerConfig struct {
	Command               []string          `json:"command,omitempty"`
	Extensions            []string          `json:"extensions,omitempty"`
	RootMarkers           []string          `json:"root_markers,omitempty"`
	LanguageID            string            `json:"language_id,omitempty"` // default derived from extension
	Env                   map[string]string `json:"env,omitempty"`
	InitializationOptions any               `json:"initialization_options,omitempty"`
	Disabled              bool              `json:"disabled,omitempty"`
}

// Builtins are enabled automatically when their binary is on PATH.
var Builtins = map[string]ServerConfig{
	"gopls":      {Command: []string{"gopls"}, Extensions: []string{".go"}, RootMarkers: []string{"go.work", "go.mod"}},
	"typescript": {Command: []string{"typescript-language-server", "--stdio"}, Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"}, RootMarkers: []string{"tsconfig.json", "jsconfig.json", "package.json"}},
	"pyright":    {Command: []string{"pyright-langserver", "--stdio"}, Extensions: []string{".py", ".pyi"}, RootMarkers: []string{"pyproject.toml", "pyrightconfig.json", "setup.py", "setup.cfg", "requirements.txt"}},
	"rust":       {Command: []string{"rust-analyzer"}, Extensions: []string{".rs"}, RootMarkers: []string{"Cargo.toml"}},
	"clangd":     {Command: []string{"clangd"}, Extensions: []string{".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".m", ".mm"}, RootMarkers: []string{"compile_commands.json", "compile_flags.txt", ".clangd", "CMakeLists.txt"}},
}

var languageIDs = map[string]string{
	".go": "go", ".ts": "typescript", ".mts": "typescript", ".cts": "typescript", ".tsx": "typescriptreact",
	".js": "javascript", ".mjs": "javascript", ".cjs": "javascript", ".jsx": "javascriptreact",
	".py": "python", ".pyi": "python", ".rs": "rust", ".c": "c", ".h": "c",
	".cc": "cpp", ".cpp": "cpp", ".cxx": "cpp", ".hpp": "cpp", ".hh": "cpp", ".m": "objective-c", ".mm": "objective-cpp",
}

// DiagnosticsTimeout bounds how long an edit waits for fresh diagnostics.
// The first check in a new server gets longer, since it loads the project.
var (
	DiagnosticsTimeout      = 3 * time.Second
	FirstDiagnosticsTimeout = 15 * time.Second
)

type Manager struct {
	cwd, root, logDir string
	servers           map[string]ServerConfig // enabled and installed

	mu       sync.Mutex
	clients  map[string]*client // name + "\x00" + root
	failed   map[string]error
	starting map[string]chan struct{}
}

// NewManager merges built-ins with configured servers. Servers whose
// command isn't installed are left out.
func NewManager(configured map[string]ServerConfig, cwd, repoRoot, logDir string) *Manager {
	root := repoRoot
	if root == "" {
		root = cwd
	}
	m := &Manager{cwd: cwd, root: root, logDir: logDir, servers: map[string]ServerConfig{},
		clients: map[string]*client{}, failed: map[string]error{}, starting: map[string]chan struct{}{}}
	merged := map[string]ServerConfig{}
	for name, cfg := range Builtins {
		merged[name] = cfg
	}
	for name, cfg := range configured {
		base, isBuiltin := merged[name]
		if isBuiltin && len(cfg.Command) == 0 { // tweak a built-in (e.g. disable it)
			base.Disabled = cfg.Disabled
			if cfg.InitializationOptions != nil {
				base.InitializationOptions = cfg.InitializationOptions
			}
			cfg = base
		}
		merged[name] = cfg
	}
	for name, cfg := range merged {
		if cfg.Disabled || len(cfg.Command) == 0 || len(cfg.Extensions) == 0 {
			continue
		}
		if _, err := exec.LookPath(cfg.Command[0]); err != nil {
			continue
		}
		m.servers[name] = cfg
	}
	return m
}

// Enabled reports whether any server is available.
func (m *Manager) Enabled() bool { return m != nil && len(m.servers) > 0 }

// serverFor picks the configured server for a file extension.
func (m *Manager) serverFor(path string) (string, ServerConfig, bool) {
	ext := strings.ToLower(filepath.Ext(path))
	names := make([]string, 0, len(m.servers))
	for n := range m.servers {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic when two servers claim an extension
	for _, n := range names {
		for _, e := range m.servers[n].Extensions {
			if strings.EqualFold(e, ext) {
				return n, m.servers[n], true
			}
		}
	}
	return "", ServerConfig{}, false
}

// projectRoot walks up from the file to the nearest root marker, staying
// inside the repository; it falls back to the repository root.
func (m *Manager) projectRoot(path string, markers []string) (string, bool) {
	rel, err := filepath.Rel(m.root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", false // outside the project: no LSP
	}
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		for _, mk := range markers {
			if _, err := os.Stat(filepath.Join(d, mk)); err == nil {
				return d, true
			}
		}
		if d == m.root || d == filepath.Dir(d) {
			break
		}
	}
	return m.root, true
}

// clientFor returns the running client for path, starting it if needed.
func (m *Manager) clientFor(ctx context.Context, path string) (*client, string, bool, error) {
	if !m.Enabled() {
		return nil, "", false, nil
	}
	name, cfg, ok := m.serverFor(path)
	if !ok {
		return nil, "", false, nil
	}
	root, ok := m.projectRoot(path, cfg.RootMarkers)
	if !ok {
		return nil, "", false, nil
	}
	key := name + "\x00" + root
	for {
		m.mu.Lock()
		if c, ok := m.clients[key]; ok && c.alive() {
			m.mu.Unlock()
			return c, languageID(path, cfg), false, nil
		}
		if err, ok := m.failed[key]; ok {
			m.mu.Unlock()
			return nil, "", false, err
		}
		if wait, ok := m.starting[key]; ok {
			m.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, "", false, ctx.Err()
			}
		}
		done := make(chan struct{})
		m.starting[key] = done
		m.mu.Unlock()

		var logPath string
		if m.logDir != "" && os.MkdirAll(m.logDir, 0o700) == nil {
			logPath = filepath.Join(m.logDir, "lsp-"+name+".log")
		}
		c, err := startClient(ctx, name, root, cfg, logPath)
		m.mu.Lock()
		delete(m.starting, key)
		switch {
		case err != nil && ctx.Err() != nil:
			// Canceled (esc) or out of time: the server may be fine, so
			// the next edit tries again.
		case err != nil:
			m.failed[key] = err // don't retry a broken server every edit
		default:
			m.clients[key] = c
		}
		close(done)
		m.mu.Unlock()
		if err != nil {
			return nil, "", false, err
		}
		return c, languageID(path, cfg), true, nil
	}
}

func languageID(path string, cfg ServerConfig) string {
	if cfg.LanguageID != "" {
		return cfg.LanguageID
	}
	ext := strings.ToLower(filepath.Ext(path))
	if id, ok := languageIDs[ext]; ok {
		return id
	}
	return strings.TrimPrefix(ext, ".")
}

// Touch opens a file in its server in the background (e.g. after a read),
// so diagnostics for a later edit arrive faster.
func (m *Manager) Touch(path string) {
	if !m.Enabled() {
		return
	}
	if _, _, ok := m.serverFor(path); !ok {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		c, lang, _, err := m.clientFor(ctx, path)
		if err != nil || c == nil {
			return
		}
		c.mu.Lock()
		_, open := c.docs[pathToURI(path)]
		c.mu.Unlock()
		if !open {
			_, _, _ = c.sync(path, lang)
		}
	}()
}

// Diagnostics syncs a just-edited file and returns a report of its errors
// and warnings, plus other files that gained errors. Empty means clean
// (or no server).
func (m *Manager) Diagnostics(ctx context.Context, path string) string {
	c, lang, fresh, err := m.clientFor(ctx, path)
	if err != nil || c == nil {
		return ""
	}
	beforeDiags, _ := c.snapshot()
	after, opened, err := c.sync(path, lang)
	if err != nil {
		return ""
	}
	timeout := DiagnosticsTimeout
	c.mu.Lock()
	quiet := c.misses >= 2
	c.mu.Unlock()
	switch {
	case fresh || time.Since(c.started) < 10*time.Second: // still loading the project
		timeout = FirstDiagnosticsTimeout
	case quiet: // this server doesn't publish for clean files; don't stall every edit
		timeout = 750 * time.Millisecond
	case opened:
		timeout = 2 * DiagnosticsTimeout
	}
	uri := pathToURI(path)
	got := c.waitFor(ctx, uri, after, timeout)
	c.mu.Lock()
	if got {
		c.misses = 0
	} else {
		c.misses++
	}
	c.mu.Unlock()

	diags, seqs := c.snapshot()
	var b strings.Builder
	if report := formatDiagnostics(m.rel(path), diags[uri], 15); report != "" {
		b.WriteString(report)
	}
	// Other files whose error count went up during this change.
	var others []string
	for u, ds := range diags {
		if u == uri || seqs[u] <= after {
			continue
		}
		if n, was := countErrors(ds), countErrors(beforeDiags[u]); n > was {
			others = append(others, fmt.Sprintf("%s (%d)", m.rel(uriToPath(u)), n))
		}
	}
	if len(others) > 0 {
		sort.Strings(others)
		if len(others) > 10 {
			others = append(others[:10], fmt.Sprintf("and %d more", len(others)-10))
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("<diagnostics>\nThis change introduced errors in other files: " + strings.Join(others, ", ") + "\n</diagnostics>")
	}
	return b.String()
}

func countErrors(ds []Diagnostic) int {
	n := 0
	for _, d := range ds {
		if d.Severity == 1 {
			n++
		}
	}
	return n
}

// formatDiagnostics lists errors, then warnings, capped at max lines.
func formatDiagnostics(file string, ds []Diagnostic, max int) string {
	var keep []Diagnostic
	for _, d := range ds {
		if d.Severity == 1 || d.Severity == 2 || d.Severity == 0 {
			keep = append(keep, d)
		}
	}
	if len(keep) == 0 {
		return ""
	}
	sort.SliceStable(keep, func(i, j int) bool {
		si, sj := sev(keep[i]), sev(keep[j])
		if si != sj {
			return si < sj
		}
		return keep[i].Range.Start.Line < keep[j].Range.Start.Line
	})
	var b strings.Builder
	fmt.Fprintf(&b, "<diagnostics file=%q>\n", file)
	for i, d := range keep {
		if i == max {
			fmt.Fprintf(&b, "... and %d more\n", len(keep)-max)
			break
		}
		label := "ERROR"
		if sev(d) == 2 {
			label = "WARN"
		}
		msg := strings.Join(strings.Fields(d.Message), " ")
		fmt.Fprintf(&b, "%s %d:%d %s", label, d.Range.Start.Line+1, d.Range.Start.Character+1, msg)
		if d.Source != "" {
			fmt.Fprintf(&b, " (%s)", d.Source)
		}
		b.WriteString("\n")
	}
	b.WriteString("</diagnostics>")
	return b.String()
}

func sev(d Diagnostic) int {
	if d.Severity == 0 {
		return 1 // unspecified: treat as error
	}
	return d.Severity
}

func (m *Manager) rel(path string) string {
	if r, err := filepath.Rel(m.cwd, path); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	return path
}

// Status is a snapshot for display.
type Status struct {
	Name      string
	Command   string
	Languages string
	Running   []string // roots with a live server
	Failed    []string
	LogPath   string
}

func (m *Manager) Statuses() []Status {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Status
	for name, cfg := range m.servers {
		st := Status{Name: name, Command: strings.Join(cfg.Command, " "), Languages: strings.Join(cfg.Extensions, " ")}
		if m.logDir != "" {
			st.LogPath = filepath.Join(m.logDir, "lsp-"+name+".log")
		}
		for key, c := range m.clients {
			if strings.HasPrefix(key, name+"\x00") && c.alive() {
				st.Running = append(st.Running, m.rel(c.root))
			}
		}
		for key, err := range m.failed {
			if strings.HasPrefix(key, name+"\x00") {
				st.Failed = append(st.Failed, err.Error())
			}
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Close shuts down every server.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	clients := make([]*client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = map[string]*client{}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *client) { defer wg.Done(); c.shutdown() }(c)
	}
	wg.Wait()
}
