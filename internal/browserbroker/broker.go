package browserbroker

import (
	"context"
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
	"time"
)

const label = "dev.larik.browser-launcher"

var ErrUnavailable = errors.New("browser launch broker is unavailable")

type request struct {
	Op       string `json:"op"`
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
	h, e := os.UserHomeDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(h, "Library", "Application Support", "Larik", "BrowserLauncher", "broker.sock"), nil
}
func plistPath() (string, error) {
	h, e := os.UserHomeDir()
	if e != nil {
		return "", e
	}
	return filepath.Join(h, "Library", "LaunchAgents", label+".plist"), nil
}
func Install() error {
	if runtime.GOOS != "darwin" {
		return errors.New("the browser launch broker is macOS-only")
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	exe, e = filepath.Abs(exe)
	if e != nil {
		return e
	}
	p, e := plistPath()
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(p), ".larik-browser-broker-*.plist")
	if e != nil {
		return e
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if e = tmp.Chmod(0600); e == nil {
		_, e = tmp.WriteString(launchAgentPlist(exe))
	}
	if e == nil {
		e = tmp.Sync()
	}
	closeErr := tmp.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	if e = os.Rename(tmpName, p); e != nil {
		return e
	}
	uid := "gui/" + strconv.Itoa(os.Getuid())
	_, _ = exec.Command("/bin/launchctl", "bootout", uid, p).CombinedOutput()
	out, e := exec.Command("/bin/launchctl", "bootstrap", uid, p).CombinedOutput()
	if e != nil {
		return fmt.Errorf("load browser LaunchAgent: %w: %s", e, strings.TrimSpace(string(out)))
	}
	return nil
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
func launchAgentPlist(exe string) string {
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", string(rune(34)), "&quot;", "'", "&apos;").Replace(s)
	}
	env := ""
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		env = "<key>EnvironmentVariables</key><dict><key>XDG_DATA_HOME</key><string>" + esc(xdg) + "</string></dict>"
	}
	return "<?xml version='1.0' encoding='UTF-8'?>\n<!DOCTYPE plist PUBLIC '-//Apple//DTD PLIST 1.0//EN' 'http://www.apple.com/DTDs/PropertyList-1.0.dtd'>\n<plist version='1.0'><dict><key>Label</key><string>" + label + "</string><key>ProgramArguments</key><array><string>" + esc(exe) + "</string><string>browser-broker</string><string>run</string></array>" + env + "<key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>StandardOutPath</key><string>/dev/null</string><key>StandardErrorPath</key><string>/dev/null</string></dict></plist>\n"
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
	if e = os.MkdirAll(d, 0700); e != nil {
		return e
	}
	if e = os.Chmod(d, 0700); e != nil {
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
	if e = os.Chmod(p, 0600); e != nil {
		return e
	}
	for {
		c, e := ln.Accept()
		if e != nil {
			return e
		}
		go handle(c)
	}
}
func handle(c net.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(20 * time.Second))
	var q request
	d := json.NewDecoder(io.LimitReader(c, 16*1024))
	d.DisallowUnknownFields()
	r := response{}
	if e := d.Decode(&q); e != nil {
		r.Error = "invalid request"
		_ = json.NewEncoder(c).Encode(r)
		return
	}
	if q.Op == "remove" {
		if e := validateTemporaryProfile(q.Profile); e != nil {
			r.Error = e.Error()
		} else if e := os.RemoveAll(q.Profile); e != nil {
			r.Error = e.Error()
		}
		_ = json.NewEncoder(c).Encode(r)
		return
	}
	if q.Op != "" && q.Op != "launch" {
		r.Error = "unsupported operation"
		_ = json.NewEncoder(c).Encode(r)
		return
	}
	createdTemp := false
	if q.Profile == "" {
		cache, e := os.UserCacheDir()
		if e != nil {
			r.Error = e.Error()
			_ = json.NewEncoder(c).Encode(r)
			return
		}
		root := filepath.Join(cache, "Larik")
		if e = os.MkdirAll(root, 0700); e != nil {
			r.Error = e.Error()
			_ = json.NewEncoder(c).Encode(r)
			return
		}
		q.Profile, e = os.MkdirTemp(root, "browser-profile-")
		if e != nil {
			r.Error = e.Error()
			_ = json.NewEncoder(c).Encode(r)
			return
		}
		createdTemp = true
	}
	if e := validate(q); e != nil {
		r.Error = e.Error()
		_ = json.NewEncoder(c).Encode(r)
		return
	}
	ws, e := launch(q)
	r.WebSocketURL, r.Profile = ws, q.Profile
	if e != nil {
		if createdTemp {
			_ = os.RemoveAll(q.Profile)
			r.Profile = ""
		}
		r.Error = e.Error()
	}
	_ = json.NewEncoder(c).Encode(r)
}
func validate(q request) error {
	name := strings.ToLower(filepath.Base(q.AppPath))
	home, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	app, e := filepath.Abs(q.AppPath)
	if e != nil || filepath.Clean(app) != q.AppPath || filepath.Ext(app) != ".app" {
		return errors.New("a clean Chrome app bundle path is required")
	}
	if (!strings.Contains(name, "chrome") && !strings.Contains(name, "chromium")) || (!underPath("/Applications", app) && !underPath(filepath.Join(home, "Applications"), app)) {
		return errors.New("only Chrome or Chromium bundles in Applications may be launched")
	}
	binDir := filepath.Join(app, "Contents", "MacOS")
	if _, e := os.Stat(binDir); e != nil {
		return errors.New("Chrome app bundle is unavailable")
	}
	validExecutable := false
	for _, executable := range []string{"Google Chrome", "Google Chrome Beta", "Google Chrome Canary", "Chromium"} {
		if _, e := os.Stat(filepath.Join(binDir, executable)); e == nil {
			validExecutable = true
			break
		}
	}
	if !validExecutable {
		return errors.New("bundle executable is not Google Chrome or Chromium")
	}
	return validateProfile(q.Profile)
}
func validateProfile(profile string) error {
	if !filepath.IsAbs(profile) || filepath.Clean(profile) != profile {
		return errors.New("profile path must be clean and absolute")
	}
	h, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	roots := []string{filepath.Join(h, "Library", "Application Support", "Larik"), filepath.Join(h, ".local", "share", "larik")}
	if xdg := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(xdg) {
		roots = append(roots, filepath.Join(xdg, "larik"))
	}
	for _, root := range roots {
		if underPath(root, profile) {
			return nil
		}
	}
	cache, _ := os.UserCacheDir()
	tmp := filepath.Clean(os.TempDir())
	if (underPath(cache, profile) && strings.HasPrefix(filepath.Base(profile), "browser-profile-")) || (underPath(tmp, profile) && strings.HasPrefix(filepath.Base(profile), "larik-browser-")) {
		return nil
	}
	return errors.New("profile must be under Larik data or a Larik browser temp directory")
}
func validateTemporaryProfile(profile string) error {
	if e := validateProfile(profile); e != nil {
		return e
	}
	cache, _ := os.UserCacheDir()
	tmp := filepath.Clean(os.TempDir())
	if (underPath(cache, profile) && strings.HasPrefix(filepath.Base(profile), "browser-profile-")) || (underPath(tmp, profile) && strings.HasPrefix(filepath.Base(profile), "larik-browser-")) {
		return nil
	}
	return errors.New("only temporary browser profiles may be removed through the broker")
}

func underPath(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func launch(q request) (string, error) {
	if e := os.MkdirAll(q.Profile, 0700); e != nil {
		return "", e
	}
	resolved, e := filepath.EvalSymlinks(q.Profile)
	if e != nil {
		return "", e
	}
	q.Profile = resolved
	if e := validate(q); e != nil {
		return "", e
	}
	active := filepath.Join(q.Profile, "DevToolsActivePort")
	_ = os.Remove(active)
	a := []string{"-n", "-a", q.AppPath, "--args", "--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--disable-sync", "--disable-features=Translate", "--metrics-recording-only", "--disable-background-timer-throttling", "--disable-backgrounding-occluded-windows", "--disable-renderer-backgrounding", "--window-size=1280,900", "--user-data-dir=" + q.Profile, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=0"}
	if q.Headless {
		a = append(a, "--headless")
	}
	out, e := exec.Command("/usr/bin/open", a...).CombinedOutput()
	if e != nil {
		return "", fmt.Errorf("open Chrome: %w: %s", e, strings.TrimSpace(string(out)))
	}
	end := time.Now().Add(12 * time.Second)
	for time.Now().Before(end) {
		b, e := os.ReadFile(active)
		if e == nil {
			v := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(v) >= 2 {
				p, x := strconv.Atoi(v[0])
				if x == nil && p > 0 && p < 65536 && strings.HasPrefix(strings.TrimSpace(v[1]), "/devtools/browser/") {
					return fmt.Sprintf("ws://127.0.0.1:%d%s", p, strings.TrimSpace(v[1])), nil
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return "", errors.New("Chrome did not publish a DevTools endpoint within 12s")
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
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
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
