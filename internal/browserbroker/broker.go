package browserbroker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const label = "dev.larik.browser-launcher"

// chromeTeamID is Google's Apple Developer Team ID. Every Google Chrome build
// is signed with it, and the broker starts nothing else: it runs outside every
// sandbox, so "a bundle named Chrome" is not a strong enough test.
const chromeTeamID = "EQHXZ8M8AV"

// appRoot is the only directory a browser may be started from. It is
// deliberately not ~/Applications, which is writable without authentication.
const appRoot = "/Applications"

// tempProfilePrefix names the throwaway profiles the broker creates, which are
// the only directories it will remove.
const tempProfilePrefix = "browser-profile-"

const (
	// launchWait is how long Chrome gets to publish its DevTools endpoint.
	// A cold start can include Gatekeeper and first-run work.
	launchWait = 20 * time.Second
	// serverDeadline must exceed launchWait, and clientDeadline must exceed
	// serverDeadline: a client that gives up first leaks the browser it asked
	// for, because it never learns the profile the broker made for it.
	serverDeadline = 45 * time.Second
	clientDeadline = 60 * time.Second
	// maxHandlers bounds the requests served at once; each can start Chrome.
	maxHandlers = 4
	leaseTTL    = 30 * time.Second
	leaseSweep  = 5 * time.Second
	brokerIdle  = 20 * time.Second
)

var ErrUnavailable = errors.New("browser launch broker is unavailable")

var leases = struct {
	sync.Mutex
	clients map[string]time.Time
}{clients: make(map[string]time.Time)}

func updateLease(op, token string) error {
	if len(token) < 32 || len(token) > 128 {
		return errors.New("invalid broker lease")
	}
	leases.Lock()
	defer leases.Unlock()
	switch op {
	case "lease":
		leases.clients[token] = time.Now()
	case "release":
		delete(leases.clients, token)
	default:
		return errors.New("unsupported lease operation")
	}
	return nil
}
func liveLeases(now time.Time) int {
	leases.Lock()
	defer leases.Unlock()
	for id, last := range leases.clients {
		if now.Sub(last) > leaseTTL {
			delete(leases.clients, id)
		}
	}
	return len(leases.clients)
}
func NewLease(ctx context.Context) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	if _, err := requestBroker(ctx, request{Op: "lease", Lease: token}); err != nil {
		return "", err
	}
	return token, nil
}
func RenewLease(ctx context.Context, token string) error {
	_, err := requestBroker(ctx, request{Op: "lease", Lease: token})
	return err
}
func ReleaseLease(ctx context.Context, token string) error {
	_, err := requestBroker(ctx, request{Op: "release", Lease: token})
	return err
}

// Replaced in tests.
var (
	userHomeDir     = os.UserHomeDir
	userCacheDir    = os.UserCacheDir
	verifySignature = codesignVerify
)

type request struct {
	Op       string `json:"op"`
	Lease    string `json:"lease,omitempty"`
	AppPath  string `json:"app_path,omitempty"`
	Profile  string `json:"profile,omitempty"`
	Headless bool   `json:"headless,omitempty"`
}
type response struct {
	WebSocketURL string `json:"websocket_url,omitempty"`
	Profile      string `json:"profile,omitempty"`
	Error        string `json:"error,omitempty"`
}

func socketPath() (string, error) {
	h, e := userHomeDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(h, "Library", "Application Support", "Larik", "BrowserLauncher", "broker.sock"), nil
}
func plistPath() (string, error) {
	h, e := userHomeDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(h, "Library", "LaunchAgents", label+".plist"), nil
}

// logPath is where the LaunchAgent's own output goes. Without it a broker that
// cannot start is invisible: launchd keeps restarting it and every request just
// reports that the broker is unavailable.
func logPath() (string, error) {
	h, e := userHomeDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(h, "Library", "Logs", "Larik", "browser-broker.log"), nil
}

// Install registers the LaunchAgent and returns what to tell the user.
func Install() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("the browser launch broker is macOS-only")
	}
	exe, e := os.Executable()
	if e != nil {
		return "", e
	}
	exe, e = filepath.Abs(exe)
	if e != nil {
		return "", e
	}
	p, e := plistPath()
	if e != nil {
		return "", e
	}
	lp, e := logPath()
	if e != nil {
		return "", e
	}
	sock, e := socketPath()
	if e != nil {
		return "", e
	}
	if e = os.MkdirAll(filepath.Dir(lp), 0o700); e != nil {
		return "", e
	}
	if e = os.MkdirAll(filepath.Dir(p), 0o700); e != nil {
		return "", e
	}
	tmp, e := os.CreateTemp(filepath.Dir(p), ".larik-browser-broker-*.plist")
	if e != nil {
		return "", e
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if e = tmp.Chmod(0o600); e == nil {
		_, e = tmp.WriteString(launchAgentPlist(exe, lp))
	}
	if e == nil {
		e = tmp.Sync()
	}
	closeErr := tmp.Close()
	if e != nil {
		return "", e
	}
	if closeErr != nil {
		return "", closeErr
	}
	if e = os.Rename(tmpName, p); e != nil {
		return "", e
	}
	uid := "gui/" + strconv.Itoa(os.Getuid())
	_, _ = exec.Command("/bin/launchctl", "bootout", uid, p).CombinedOutput()
	out, e := exec.Command("/bin/launchctl", "bootstrap", uid, p).CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("load browser LaunchAgent: %w: %s", e, strings.TrimSpace(string(out)))
	}
	if e = waitForSocket(sock, 5*time.Second); e != nil {
		return "", fmt.Errorf("%w (see %s)", e, lp)
	}
	msg := "browser launch broker installed, listening on " + sock + "\nit logs to " + lp + "\nit starts only a Google-signed Chrome in " + appRoot
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		msg += "\nXDG_DATA_HOME is recorded in the LaunchAgent: run this again if you change it"
	}
	msg += "\nit runs " + exe + ": run this again if you move or reinstall larik"
	return msg, nil
}

// waitForSocket waits for the broker to start listening, so a LaunchAgent that
// crash-loops is reported at install time instead of at the first browser call.
func waitForSocket(sock string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if c, e := net.DialTimeout("unix", sock, 250*time.Millisecond); e == nil {
			_ = c.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the browser LaunchAgent was loaded but is not listening on %s", sock)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func Uninstall() error {
	if runtime.GOOS != "darwin" {
		return errors.New("the browser launch broker is macOS-only")
	}
	p, e := plistPath()
	if e != nil {
		return e
	}
	out, e := exec.Command("/bin/launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid()), p).CombinedOutput()
	if e != nil && !strings.Contains(string(out), "Could not find specified service") {
		return fmt.Errorf("unload browser LaunchAgent: %w: %s", e, strings.TrimSpace(string(out)))
	}
	if e = os.Remove(p); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return nil
}
func launchAgentPlist(exe, log string) string {
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", string(rune(34)), "&quot;", "'", "&apos;").Replace(s)
	}
	env := ""
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		env = "<key>EnvironmentVariables</key><dict><key>XDG_DATA_HOME</key><string>" + esc(xdg) + "</string></dict>"
	}
	return "<?xml version='1.0' encoding='UTF-8'?>\n<!DOCTYPE plist PUBLIC '-//Apple//DTD PLIST 1.0//EN' 'http://www.apple.com/DTDs/PropertyList-1.0.dtd'>\n<plist version='1.0'><dict><key>Label</key><string>" + label + "</string><key>ProgramArguments</key><array><string>" + esc(exe) + "</string><string>browser-broker</string><string>run</string></array>" + env + "<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>StandardOutPath</key><string>" + esc(log) + "</string><key>StandardErrorPath</key><string>" + esc(log) + "</string></dict></plist>\n"
}
func Serve() error {
	if runtime.GOOS != "darwin" {
		return errors.New("the browser launch broker is macOS-only")
	}
	p, e := socketPath()
	if e != nil {
		return e
	}
	d := filepath.Dir(p)
	if e = os.MkdirAll(d, 0o700); e != nil {
		return e
	}
	if e = os.Chmod(d, 0o700); e != nil {
		return e
	}
	if fi, e := os.Lstat(p); e == nil {
		if fi.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket %s", p)
		}
		if c, dialErr := net.DialTimeout("unix", p, 250*time.Millisecond); dialErr == nil {
			_ = c.Close()
			return errors.New("browser launch broker is already running")
		}
		if e = os.Remove(p); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	ln, e := net.Listen("unix", p)
	if e != nil {
		return e
	}
	defer func() { ln.Close(); os.Remove(p) }()
	if e = os.Chmod(p, 0o600); e != nil {
		return e
	}
	sem := make(chan struct{}, maxHandlers)
	_ = ln.(*net.UnixListener).SetDeadline(time.Now().Add(time.Second))
	ticker := time.NewTicker(leaseSweep)
	defer ticker.Stop()
	idleSince := time.Now()
	for {
		c, e := ln.Accept()
		if e != nil {
			if ne, ok := e.(net.Error); ok && ne.Timeout() {
				if liveLeases(time.Now()) > 0 || len(sem) > 0 {
					idleSince = time.Time{}
					continue
				}
				select {
				case <-ticker.C:
				default:
				}
				if idleSince.IsZero() {
					idleSince = time.Now()
				}
				if time.Since(idleSince) >= brokerIdle {
					if err := Uninstall(); err != nil {
						return err
					}
					return nil
				}
				continue
			}
			return e
		}
		idleSince = time.Time{}
		select {
		case sem <- struct{}{}:
			go func() { defer func() { <-sem }(); handle(c) }()
		default:
			_ = c.SetDeadline(time.Now().Add(2 * time.Second))
			_ = json.NewEncoder(c).Encode(response{Error: "the browser launch broker is busy"})
			_ = c.Close()
		}
	}
}
func handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(serverDeadline))
	var q request
	d := json.NewDecoder(io.LimitReader(c, 16*1024))
	d.DisallowUnknownFields()
	r := response{}
	reply := func() { _ = json.NewEncoder(c).Encode(r) }
	if e := d.Decode(&q); e != nil {
		r.Error = "invalid request"
		reply()
		return
	}
	switch q.Op {
	case "lease", "release":
		if e := updateLease(q.Op, q.Lease); e != nil {
			r.Error = e.Error()
		}
		reply()
		return
	case "remove":
		if e := validateTemporaryProfile(q.Profile); e != nil {
			r.Error = e.Error()
		} else if e := os.RemoveAll(q.Profile); e != nil {
			r.Error = e.Error()
		}
		reply()
		return
	case "", "launch":
	default:
		r.Error = "unsupported operation"
		reply()
		return
	}
	// The bundle is checked before anything is created, so a request the
	// broker refuses leaves no directory behind.
	app, e := validateApp(q.AppPath)
	if e != nil {
		r.Error = e.Error()
		reply()
		return
	}
	createdTemp := false
	if q.Profile == "" {
		dir, e := makeTempProfile()
		if e != nil {
			r.Error = e.Error()
			reply()
			return
		}
		q.Profile, createdTemp = dir, true
	}
	ws, e := launch(app, q)
	r.WebSocketURL, r.Profile = ws, q.Profile
	if e != nil {
		if createdTemp {
			_ = os.RemoveAll(q.Profile)
			r.Profile = ""
		}
		r.WebSocketURL, r.Error = "", e.Error()
	}
	reply()
}

// validateApp checks appPath is an intact, Google-signed Chrome in
// /Applications and returns the resolved bundle, which is what gets launched:
// a symlink swapped after the check must not change what starts.
func validateApp(appPath string) (string, error) {
	app, e := filepath.Abs(appPath)
	if e != nil || filepath.Clean(app) != appPath || filepath.Ext(app) != ".app" {
		return "", errors.New("a clean Chrome app bundle path is required")
	}
	name := strings.ToLower(filepath.Base(app))
	if !strings.Contains(name, "chrome") && !strings.Contains(name, "chromium") {
		return "", errors.New("only Chrome or Chromium bundles may be launched")
	}
	resolved, e := filepath.EvalSymlinks(app)
	if e != nil {
		return "", errors.New("Chrome app bundle is unavailable")
	}
	if !underPath(appRoot, app) || !underPath(appRoot, resolved) {
		return "", errors.New("only Chrome bundles in " + appRoot + " may be launched")
	}
	binDir := filepath.Join(resolved, "Contents", "MacOS")
	validExecutable := false
	for _, executable := range []string{"Google Chrome", "Google Chrome Beta", "Google Chrome Canary", "Chromium"} {
		if _, e := os.Stat(filepath.Join(binDir, executable)); e == nil {
			validExecutable = true
			break
		}
	}
	if !validExecutable {
		return "", errors.New("bundle executable is not Google Chrome or Chromium")
	}
	if e := verifySignature(resolved); e != nil {
		return "", e
	}
	return resolved, nil
}

// codesignVerify accepts only a bundle whose signature is intact and made by
// Google. An unsigned or self-built Chromium is refused; larik then starts it
// with its own launcher instead, which has no extra privileges.
func codesignVerify(app string) error {
	req := "=anchor apple generic and certificate leaf[subject.OU] = " + chromeTeamID
	out, e := exec.Command("/usr/bin/codesign", "--verify", "--strict", "-R", req, "--", app).CombinedOutput()
	if e != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = e.Error()
		}
		return fmt.Errorf("%s is not an intact, Google-signed Chrome: %s", filepath.Base(app), detail)
	}
	return nil
}

// tempProfileRoot holds the throwaway profiles the broker creates.
func tempProfileRoot() (string, error) {
	cache, e := userCacheDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(cache, "Larik"), nil
}

func makeTempProfile() (string, error) {
	root, e := tempProfileRoot()
	if e != nil {
		return "", e
	}
	if e = os.MkdirAll(root, 0o700); e != nil {
		return "", e
	}
	return os.MkdirTemp(root, tempProfilePrefix)
}

// profileRoots are the directories a persistent profile may live in: Larik's
// data directory, however it is configured. Each is listed resolved as well as
// written, so a symlinked ~/.local/share neither hides a profile from the check
// nor makes a valid one look like it is somewhere else.
func profileRoots() ([]string, error) {
	h, e := userHomeDir()
	if e != nil {
		return nil, e
	}
	roots := []string{filepath.Join(h, ".local", "share", "larik")}
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		roots = append(roots, filepath.Join(filepath.Clean(xdg), "larik"))
	}
	out := make([]string, 0, len(roots)*2)
	for _, root := range roots {
		out = append(out, root)
		if resolved := resolvePath(root); resolved != root {
			out = append(out, resolved)
		}
	}
	return out, nil
}

// resolvePath returns path with the symlinks in its existing prefix resolved,
// keeping the part that does not exist yet. Without it a profile written in
// one form is compared against roots in another and wrongly refused: on macOS
// /var is a link to /private/var, and a data directory can be a link too.
func resolvePath(path string) string {
	rest := ""
	for cur := filepath.Clean(path); ; {
		if r, e := filepath.EvalSymlinks(cur); e == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return filepath.Clean(path)
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func validateProfile(profile string) error {
	if !filepath.IsAbs(profile) || filepath.Clean(profile) != profile {
		return errors.New("profile path must be clean and absolute")
	}
	roots, e := profileRoots()
	if e != nil {
		return e
	}
	for _, root := range roots {
		for _, candidate := range []string{profile, resolvePath(profile)} {
			if underPath(root, candidate) {
				return nil
			}
		}
	}
	if isTemporaryProfile(profile) {
		return nil
	}
	return errors.New("profile must be under the Larik data directory or a Larik temporary browser profile")
}

// isTemporaryProfile reports whether profile is one of the throwaway profiles
// the broker creates: directly inside the cache root, under its own prefix.
// The parent is pinned, so no other directory that happens to sit somewhere
// under the cache can be passed off as one.
func isTemporaryProfile(profile string) bool {
	root, e := tempProfileRoot()
	if e != nil {
		return false
	}
	resolved := resolvePath(root)
	if !strings.HasPrefix(filepath.Base(profile), tempProfilePrefix) {
		return false
	}
	for _, dir := range []string{filepath.Dir(profile), filepath.Dir(resolvePath(profile))} {
		if dir == root || dir == resolved {
			return true
		}
	}
	return false
}

func validateTemporaryProfile(profile string) error {
	if e := validateProfile(profile); e != nil {
		return e
	}
	if !isTemporaryProfile(profile) {
		return errors.New("only temporary browser profiles may be removed through the broker")
	}
	return nil
}

func underPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func launch(app string, q request) (string, error) {
	// Validate before creating anything, so a refused path is never made.
	if e := validateProfile(q.Profile); e != nil {
		return "", e
	}
	if e := os.MkdirAll(q.Profile, 0o700); e != nil {
		return "", e
	}
	// Chrome resolves the profile path anyway; resolve it here too, so a
	// symlink in it cannot put the profile outside the allowed roots.
	resolved, e := filepath.EvalSymlinks(q.Profile)
	if e != nil {
		return "", e
	}
	q.Profile = resolved
	if e := validateProfile(q.Profile); e != nil {
		return "", e
	}
	active := filepath.Join(q.Profile, "DevToolsActivePort")
	_ = os.Remove(active)
	a := []string{"-n"}
	if q.Headless {
		a = append(a, "-g") // a browser nobody sees must not take focus
	}
	a = append(a, "-a", app, "--args", "--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--disable-sync", "--disable-features=Translate", "--metrics-recording-only", "--disable-background-timer-throttling", "--disable-backgrounding-occluded-windows", "--disable-renderer-backgrounding", "--window-size=1280,900", "--user-data-dir="+q.Profile, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0")
	if q.Headless {
		a = append(a, "--headless")
	}
	out, e := exec.Command("/usr/bin/open", a...).CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("open Chrome: %w: %s", e, strings.TrimSpace(string(out)))
	}
	end := time.Now().Add(launchWait)
	for time.Now().Before(end) {
		b, e := os.ReadFile(active)
		if e == nil {
			v := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(v) >= 2 {
				p, x := strconv.Atoi(strings.TrimSpace(v[0]))
				if x == nil && p > 0 && p < 65536 && strings.HasPrefix(strings.TrimSpace(v[1]), "/devtools/browser/") {
					return fmt.Sprintf("ws://127.0.0.1:%d%s", p, strings.TrimSpace(v[1])), nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", fmt.Errorf("Chrome did not publish a DevTools endpoint within %s", launchWait)
}
func Launch(ctx context.Context, app, profile string, headless bool) (string, string, error) {
	r, e := requestBroker(ctx, request{Op: "launch", AppPath: app, Profile: profile, Headless: headless})
	if e != nil {
		return "", "", e
	}
	if !strings.HasPrefix(r.WebSocketURL, "ws://127.0.0.1:") || !strings.Contains(r.WebSocketURL, "/devtools/browser/") {
		return "", "", errors.New("broker returned invalid DevTools endpoint")
	}
	return r.WebSocketURL, r.Profile, nil
}
func RemoveProfile(ctx context.Context, profile string) error {
	_, e := requestBroker(ctx, request{Op: "remove", Profile: profile})
	return e
}

// requestBroker sends one request. ctx bounds connecting; the exchange itself
// runs to clientDeadline, which is longer than the broker's own deadline:
// giving up first would leak the browser and the profile it made for us.
func requestBroker(ctx context.Context, q request) (response, error) {
	p, e := socketPath()
	if e != nil {
		return response{}, e
	}
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", p)
	if e != nil {
		return response{}, fmt.Errorf("%w: %v", ErrUnavailable, e)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(clientDeadline))
	if e = json.NewEncoder(c).Encode(q); e != nil {
		return response{}, e
	}
	var r response
	if e = json.NewDecoder(c).Decode(&r); e != nil {
		return response{}, e
	}
	if r.Error != "" {
		return response{}, errors.New(r.Error)
	}
	return r, nil
}
