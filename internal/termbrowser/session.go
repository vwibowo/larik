// Package termbrowser drives a headless Chrome over the DevTools Protocol
// for the browser_* tools, and shows its pages inside the terminal: a
// viewer (larik browser view) in a split pane attaches to the same Chrome,
// draws the tab the agent is working in with the kitty graphics protocol
// and forwards the user's mouse and keys to it. Chrome starts on first use
// and stays up for the rest of the process.
package termbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"

	"larik/internal/textutil"
	"larik/internal/tools"
	"larik/internal/web"
)

// Options configure the browser.
type Options struct {
	ChromePath string // empty: find Chrome or Chromium
	ProfileDir string // persistent profile, so sign-ins survive restarts
	// DownloadDir receives files the pages download; empty means a
	// temporary directory.
	DownloadDir string
	// SocketDir holds the control socket the viewer connects to.
	SocketDir string
	// PaneSize is the share of the terminal's width, in percent, the
	// viewer's pane takes (default 50).
	PaneSize int
	// LogFile receives chromedp's own messages; empty drops them. They
	// must never reach stderr, which is the TUI's screen.
	LogFile string
	// Sound lets pages play audio on this computer; otherwise Chrome is
	// muted. Chrome can't be unmuted once started, so it applies from the
	// next start.
	Sound bool
	// Location is the position a page gets when the user allows it to ask
	// (browser.location); headless Chrome can't read the computer's own.
	Location *Location
}

const (
	actionTimeout = 30 * time.Second
	// closeTimeout bounds waiting for Chrome to quit cleanly before it is
	// killed.
	closeTimeout          = 5 * time.Second
	browserStartupTimeout = 12 * time.Second
	maxConsole            = 200
	maxRequests           = 300
)

// Session is one Chrome process and its tabs. Each agent has its own tabs
// and its own active tab (keyed by tools.Owner), so subagents browsing at
// the same time don't navigate each other's pages.
type Session struct {
	opts Options

	mu          sync.Mutex
	cancelAlloc context.CancelFunc
	root        context.Context // the browser's first tab; parent of the others
	cancelRoot  context.CancelFunc
	tabs        []*tab
	active      map[string]*tab // owner -> its active tab

	// wsURL is the browser's DevTools endpoint, for the viewer.
	wsURL string
	// shower opens a viewer inside the host program (SetShower).
	shower func() (string, bool)

	// Pane-launch seams keep tests independent of the real terminal. They
	// are set before the session is used; nil selects production behavior.
	paneRun func(context.Context, []string) error
	paneEnv func(string) string

	// startupTimeout bounds Chrome launch and the initial DevTools guard.
	startupTimeout time.Duration

	// tempProfile is a throwaway profile used when ProfileDir is taken by
	// another Chrome; it's removed on shutdown.
	tempProfile string
	downloadDir string
	downloads   map[string]string // download GUID -> suggested file name

	// view is what the viewer shows and the control socket reports.
	view viewState

	// notes are told to the model with the next tool result: a fallback
	// profile, finished downloads.
	notesMu sync.Mutex
	notes   []string

	// hosts caches web.CheckHost per host for the request guard.
	hostsMu sync.Mutex
	hosts   map[string]error
}

func (s *Session) note(format string, args ...any) {
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	s.notes = append(s.notes, fmt.Sprintf(format, args...))
}

// takeNotes returns and clears the pending notes.
func (s *Session) takeNotes() []string {
	s.notesMu.Lock()
	defer s.notesMu.Unlock()
	n := s.notes
	s.notes = nil
	return n
}

type tab struct {
	id     target.ID
	owner  string // the agent it belongs to; "" is the main agent
	ctx    context.Context
	cancel context.CancelFunc

	// navs counts navigations started, so an action can tell whether it
	// set one off.
	navs atomic.Int64
	// parsed counts documents parsed (DOMContentLoaded), so Navigate can
	// wait for the new page without waiting for its load event.
	parsed atomic.Int64
	// chooser receives file choosers the page opens while an upload waits
	// (uploading); other choosers are noted and left unanswered.
	chooser   chan *page.EventFileChooserOpened
	uploading atomic.Bool

	mu       sync.Mutex
	console  []string
	requests []*request // recent network requests, oldest first
	nextReq  int
}

// request is one network request a tab made.
type request struct {
	N        int // number shown to the model
	id       network.RequestID
	Method   string
	URL      string
	Type     string // document, xhr, fetch, script, image…
	Status   int64  // 0 until a response arrives
	MIME     string
	Failed   string // error text, e.g. net::ERR_NAME_NOT_RESOLVED
	started  time.Time
	Duration time.Duration
	done     bool
}

func (t *tab) log(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.console = append(t.console, s)
	if len(t.console) > maxConsole {
		t.console = t.console[len(t.console)-maxConsole:]
	}
}

func New(opts Options) *Session {
	return &Session{opts: opts, startupTimeout: browserStartupTimeout}
}

// noBrowser is the advice given when no Chromium-based browser is found.
const noBrowser = "install a Chromium-based browser (Chrome, Chromium, Edge, Brave or Vivaldi) or set browser.chrome_path"

// FindChrome reports whether a Chromium-based browser that the browser tools
// can drive is installed, without starting it. The browser is launched on the
// first browser tool call, so this is only used for an early warning: a path
// this misses still works, since chromedp finds Chrome on its own.
func FindChrome(opts Options) error {
	if opts.ChromePath != "" {
		if _, err := exec.LookPath(opts.ChromePath); err != nil {
			return fmt.Errorf("browser.chrome_path %q: %w", opts.ChromePath, err)
		}
		return nil
	}
	if chromeBinary() != "" {
		return nil
	}
	return errors.New("no Chromium-based browser found; " + noBrowser)
}

// chromeBinary is the first installed Chromium-based browser in
// browserCandidates' order of preference, or "" to leave the search to
// chromedp. They all speak the DevTools Protocol, and each runs on Larik's
// own profile, so the user's profile for that browser is never touched.
func chromeBinary() string {
	home, _ := os.UserHomeDir()
	return firstInstalled(browserCandidates(goruntime.GOOS, home, os.Getenv))
}

// browserCandidates lists where a Chromium-based browser is usually installed
// on goos, Chrome first, then Chromium, Edge, Brave and Vivaldi. An entry is
// an absolute path or, on Linux, a name to look up on PATH.
func browserCandidates(goos, home string, getenv func(string) string) []string {
	switch goos {
	case "darwin":
		var paths []string
		for _, app := range []string{
			"Google Chrome", "Google Chrome Beta", "Google Chrome Canary", "Chromium",
			"Microsoft Edge", "Brave Browser", "Vivaldi",
		} {
			bin := filepath.Join(app+".app", "Contents", "MacOS", app)
			paths = append(paths, filepath.Join("/Applications", bin))
			if home != "" {
				paths = append(paths, filepath.Join(home, "Applications", bin))
			}
		}
		return paths
	case "windows":
		var paths []string
		for _, exe := range []string{
			`Google\Chrome\Application\chrome.exe`,
			`Chromium\Application\chrome.exe`,
			`Microsoft\Edge\Application\msedge.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
			`Vivaldi\Application\vivaldi.exe`,
		} {
			for _, dir := range []string{getenv("ProgramFiles"), getenv("ProgramFiles(x86)"), getenv("LOCALAPPDATA")} {
				if dir != "" {
					paths = append(paths, dir+`\`+exe)
				}
			}
		}
		return paths
	default:
		return []string{
			"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "/snap/bin/chromium",
			"microsoft-edge", "microsoft-edge-stable", "brave-browser", "brave", "vivaldi", "vivaldi-stable",
		}
	}
}

// firstInstalled is the first candidate that exists: a regular file for an
// absolute path, an executable on PATH for a bare name. It returns "" if none
// does.
func firstInstalled(candidates []string) string {
	for _, c := range candidates {
		if filepath.IsAbs(c) || strings.ContainsAny(c, `/\`) {
			if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
				return c
			}
			continue
		}
		if path, err := exec.LookPath(c); err == nil {
			return path
		}
	}
	return ""
}

// Close quits Chrome, if it was started.
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.stopServing()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdown()
}

func (s *Session) shutdown() {
	if s.cancelRoot != nil {
		// Ask Chrome to quit and wait for it, rather than killing it:
		// cookies and localStorage are written out on the way down, and a
		// killed Chrome loses whatever it hadn't saved yet.
		if s.root.Err() == nil {
			ctx, cancel := context.WithTimeout(s.root, closeTimeout)
			_ = chromedp.Cancel(ctx)
			cancel()
		}
		s.cancelRoot()
		s.cancelAlloc()
	}
	s.root, s.cancelRoot, s.cancelAlloc, s.wsURL = nil, nil, nil, ""
	s.tabs, s.active = nil, nil
	if s.tempProfile != "" {
		_ = os.RemoveAll(s.tempProfile)
		s.tempProfile = ""
	}
	s.changed()
}

// start launches Chrome unless it's running. The contexts derive from
// Background: a tool call's context ending must not close the browser.
func (s *Session) start() error {
	if s.root != nil && s.root.Err() == nil {
		return nil
	}
	s.shutdown() // the window was closed; start over

	profile := s.opts.ProfileDir
	if profile != "" && profileInUse(profile) {
		profile = ""
	}
	err := s.launch(profile)
	if err != nil && s.opts.ProfileDir != "" && profile != "" {
		// Chrome may have handed off to one already running on the
		// profile (where the lock check can't tell, e.g. Windows).
		profile = ""
		err = s.launch("")
	}
	if err != nil {
		return err
	}
	if s.opts.ProfileDir != "" && profile == "" {
		s.note("The browser profile is in use by another Chrome (probably another larik session), so this browser uses a fresh temporary profile: sites you signed in to there aren't signed in here.")
	}
	return nil
}

// ensureProfile returns profile, or a temporary profile directory when it is
// empty, remembered for removal on shutdown.
func (s *Session) ensureProfile(profile string) (string, error) {
	if profile != "" {
		return profile, nil
	}
	if s.tempProfile != "" {
		return s.tempProfile, nil // a launcher that already failed made it
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	profileRoot := filepath.Join(cacheDir, "Larik")
	if err := os.MkdirAll(profileRoot, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(profileRoot, "browser-profile-")
	if err != nil {
		return "", err
	}
	s.tempProfile = dir
	return dir, nil
}

// launch starts Chrome with profile, or a temporary profile when empty.
// Chrome always runs headless: the user sees its pages in the terminal.
func (s *Session) launch(profile string) error {
	startupTimeout := s.startupTimeout
	if startupTimeout <= 0 {
		startupTimeout = browserStartupTimeout
	}
	profile, err := s.ensureProfile(profile)
	if err != nil {
		return err
	}
	if err := s.launchExec(profile, startupTimeout); err != nil {
		if s.tempProfile != "" {
			_ = os.RemoveAll(s.tempProfile)
			s.tempProfile = ""
		}
		return fmt.Errorf("starting the browser: %w (%s)", err, noBrowser)
	}
	return nil
}

// launchExec starts Chrome as a child process and connects over DevTools.
func (s *Session) launchExec(profile string, startupTimeout time.Duration) error {
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-sync", true),
		chromedp.Flag("disable-features", "Translate"),
		chromedp.Flag("metrics-recording-only", true),
		// Agents work in tabs that aren't in front; keep those running at
		// full speed.
		chromedp.Flag("disable-background-timer-throttling", true),
		chromedp.Flag("disable-backgrounding-occluded-windows", true),
		chromedp.Flag("disable-renderer-backgrounding", true),
		chromedp.WindowSize(1280, 900),
		chromedp.Flag("headless", "new"),
		chromedp.Flag("hide-scrollbars", true),
		// Scroll as the wheel events come: an animated scroll restarted by
		// every trackpad tick trails behind the user's fingers.
		chromedp.Flag("disable-smooth-scrolling", true),
	}
	if !s.opts.Sound {
		opts = append(opts, chromedp.Flag("mute-audio", true))
	}
	// Render at 2x natively: the screencast then sends full-resolution
	// frames for a Retina-class terminal, which a scale factor set over
	// DevTools doesn't (it sends CSS-pixel frames). Screenshots clip by
	// devicePixelRatio, so the model's images stay one pixel per CSS pixel.
	opts = append(opts, chromedp.Flag("force-device-scale-factor", "2"))
	if path := firstNonEmpty(s.opts.ChromePath, chromeBinary()); path != "" {
		opts = append(opts, chromedp.ExecPath(path))
	}
	opts = append(opts, chromedp.UserDataDir(profile))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	logf := s.logger()
	root, cancelRoot := chromedp.NewContext(allocCtx, chromedp.WithLogf(logf), chromedp.WithErrorf(logf))
	t := &tab{ctx: root}
	s.watch(t)
	// The first Run allocates Chrome using the context passed to it. Running it
	// with a short-lived child context would kill Chrome as soon as that
	// context is canceled, even after a successful launch. Bound startup by
	// canceling the long-lived root only if the deadline expires.
	stopStartupTimer := time.AfterFunc(startupTimeout, cancelRoot)
	err := chromedp.Run(root, s.guard())
	if !stopStartupTimer.Stop() && err != nil {
		err = fmt.Errorf("no response within %s: %w", startupTimeout, context.DeadlineExceeded)
	}
	if err != nil {
		cancelRoot()
		cancelAlloc()
		return err
	}
	s.root, s.cancelRoot, s.cancelAlloc = root, cancelRoot, cancelAlloc
	t.id = chromedp.FromContext(root).Target.TargetID
	s.tabs, s.active = []*tab{t}, map[string]*tab{"": t}
	s.wsURL = devToolsURL(profile)
	s.setupDownloads()
	s.changed()
	return nil
}

// logger writes chromedp's messages to the log file. chromedp reports, for
// instance, every DOM event newer than it knows ("unhandled node event"),
// which is noise to the user and must not land on the terminal.
func (s *Session) logger() func(string, ...any) {
	path := s.opts.LogFile
	if path == "" {
		return func(string, ...any) {}
	}
	var mu sync.Mutex
	var f *os.File
	return func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if f == nil {
			if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
				return
			}
			flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
			if fi, err := os.Stat(path); err == nil && fi.Size() > 1<<20 {
				flags |= os.O_TRUNC // keep it from growing without bound
			}
			var err error
			if f, err = os.OpenFile(path, flags, 0o600); err != nil {
				return
			}
		}
		fmt.Fprintf(f, time.Now().Format("2006-01-02 15:04:05 ")+format+"\n", args...)
	}
}

// devToolsURL reads the browser endpoint Chrome publishes in its profile
// once it listens, or "" when it hasn't.
func devToolsURL(profile string) string {
	data, err := os.ReadFile(filepath.Join(profile, "DevToolsActivePort"))
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		return ""
	}
	port, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	path := strings.TrimSpace(lines[1])
	if err != nil || port <= 0 || port > 65535 || !strings.HasPrefix(path, "/devtools/browser/") {
		return ""
	}
	return fmt.Sprintf("ws://127.0.0.1:%d%s", port, path)
}

// profileLockPID is the Chrome holding dir, from the SingletonLock symlink to
// "host-pid" that it keeps while it runs (macOS and Linux), or 0. Chrome
// removes the lock when it exits, so a process we want to stop has to be read
// while it is still attached.
func profileLockPID(dir string) int {
	link, err := os.Readlink(filepath.Join(dir, "SingletonLock"))
	if err != nil {
		return 0
	}
	i := strings.LastIndexByte(link, '-')
	if i < 0 {
		return 0
	}
	var pid int
	if _, err := fmt.Sscan(link[i+1:], &pid); err != nil || pid <= 0 {
		return 0
	}
	if host, _ := os.Hostname(); host != "" && link[:i] != host {
		return 0 // locked from another machine (a shared home directory)
	}
	return pid
}

// profileInUse reports whether a running Chrome holds the profile.
func profileInUse(dir string) bool {
	pid := profileLockPID(dir)
	return pid > 0 && processAlive(pid)
}

// guard turns on request interception for a tab, so every request (not
// just the URL browser_navigate opens) passes the address check.
// It also intercepts file choosers, which would otherwise open a native
// dialog nobody can answer; browser_upload fills them instead.
func (s *Session) guard() chromedp.Action {
	return chromedp.Tasks{
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}}).
			WithHandleAuthRequests(true), // password prompts go to the viewer (auth.go)
		page.SetInterceptFileChooserDialog(true),
		network.Enable(), // for the request log
	}
}

// checkRequest lets a paused request through unless its host is, or
// resolves to, a blocked address. Results are cached per host.
func (s *Session) checkRequest(t *tab, ev *fetch.EventRequestPaused) {
	ctx := cdp.WithExecutor(t.ctx, chromedp.FromContext(t.ctx).Target)
	u, err := url.Parse(ev.Request.URL)
	if err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "ws" || u.Scheme == "wss") {
		host := strings.ToLower(u.Hostname())
		s.hostsMu.Lock()
		if s.hosts == nil {
			s.hosts = map[string]error{}
		}
		verdict, seen := s.hosts[host]
		s.hostsMu.Unlock()
		if !seen {
			lookup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			verdict = web.CheckHost(lookup, host)
			cancel()
			s.hostsMu.Lock()
			s.hosts[host] = verdict
			s.hostsMu.Unlock()
		}
		if verdict != nil {
			t.log(fmt.Sprintf("[blocked] %s (%v)", ev.Request.URL, verdict))
			_ = fetch.FailRequest(ev.RequestID, network.ErrorReasonBlockedByClient).Do(ctx)
			return
		}
	}
	_ = fetch.ContinueRequest(ev.RequestID).Do(ctx)
}

// setupDownloads saves downloads into the download directory under their
// own names and tells the model when each finishes.
func (s *Session) setupDownloads() {
	dir := s.opts.DownloadDir
	if dir == "" {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("larik-downloads-%d", os.Getpid()))
	}
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	s.downloadDir, s.downloads = dir, map[string]string{}
	var mu sync.Mutex
	chromedp.ListenBrowser(s.root, func(ev any) {
		switch ev := ev.(type) {
		case *browser.EventDownloadWillBegin:
			mu.Lock()
			s.downloads[ev.GUID] = ev.SuggestedFilename
			mu.Unlock()
		case *browser.EventDownloadProgress:
			if ev.State != browser.DownloadProgressStateCompleted && ev.State != browser.DownloadProgressStateCanceled {
				return
			}
			mu.Lock()
			name := s.downloads[ev.GUID]
			delete(s.downloads, ev.GUID)
			mu.Unlock()
			if ev.State == browser.DownloadProgressStateCanceled {
				s.note("A download (%s) was canceled.", name)
				return
			}
			s.note("Downloaded %s to %s.", name, finishDownload(dir, ev.GUID, name))
		}
	})
	ctx := cdp.WithExecutor(s.root, chromedp.FromContext(s.root).Browser)
	_ = browser.SetDownloadBehavior(browser.SetDownloadBehaviorBehaviorAllowAndName).
		WithDownloadPath(dir).WithEventsEnabled(true).Do(ctx)
}

// finishDownload renames a download (saved under its GUID) to its
// suggested name, without overwriting an earlier file, and returns its path.
func finishDownload(dir, guid, name string) string {
	from := filepath.Join(dir, guid)
	name = filepath.Base(name)
	if name == "" || name == "." || name == string(filepath.Separator) {
		return from
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	to := filepath.Join(dir, name)
	for i := 1; ; i++ {
		if _, err := os.Lstat(to); errors.Is(err, os.ErrNotExist) {
			break
		}
		to = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
	}
	if os.Rename(from, to) != nil {
		return from
	}
	return to
}

// watch buffers a tab's console output and dismisses its dialogs, which
// would otherwise block the page (and every later call) until answered.
func (s *Session) watch(t *tab) {
	t.chooser = make(chan *page.EventFileChooserOpened, 1)
	chromedp.ListenTarget(t.ctx, func(ev any) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			var args []string
			for _, a := range ev.Args {
				var str string
				switch {
				case a.Value == nil:
					args = append(args, a.Description)
				case json.Unmarshal(a.Value, &str) == nil:
					args = append(args, str)
				default:
					args = append(args, string(a.Value))
				}
			}
			t.log(fmt.Sprintf("[%s] %s", ev.Type, strings.Join(args, " ")))
		case *runtime.EventExceptionThrown:
			msg := ev.ExceptionDetails.Text
			if ex := ev.ExceptionDetails.Exception; ex != nil && ex.Description != "" {
				msg = ex.Description
			}
			t.log("[exception] " + textutil.FirstLine(msg))
		case *page.EventFrameRequestedNavigation, *page.EventFrameStartedLoading:
			t.navs.Add(1)
		case *network.EventRequestWillBeSent:
			t.requestSent(ev)
		case *network.EventResponseReceived:
			t.update(ev.RequestID, func(r *request) { r.Status, r.MIME = ev.Response.Status, ev.Response.MimeType })
		case *network.EventLoadingFinished:
			t.update(ev.RequestID, func(r *request) { r.done, r.Duration = true, time.Since(r.started) })
		case *network.EventLoadingFailed:
			t.update(ev.RequestID, func(r *request) {
				r.done, r.Duration, r.Failed = true, time.Since(r.started), ev.ErrorText
				if ev.BlockedReason != "" {
					r.Failed += " (blocked: " + string(ev.BlockedReason) + ")"
				}
			})
		case *fetch.EventRequestPaused:
			go s.checkRequest(t, ev)
		case *fetch.EventAuthRequired:
			go s.handleAuth(t, ev)
		case *page.EventDomContentEventFired:
			t.parsed.Add(1)
		case *page.EventFileChooserOpened:
			if !t.uploading.Load() {
				s.note("The page opened a file chooser. To choose files, call browser_upload with the ref you clicked.")
				return
			}
			select {
			case t.chooser <- ev:
			default:
			}
		case *page.EventJavascriptDialogOpening:
			// Alerts are accepted, and so is leaving a page (declining a
			// beforeunload would cancel the navigation); confirm and prompt
			// are declined.
			accept := ev.Type == page.DialogTypeAlert || ev.Type == page.DialogTypeBeforeunload
			t.log(fmt.Sprintf("[dialog %s] %s (%s automatically)", ev.Type, ev.Message, map[bool]string{true: "accepted", false: "dismissed"}[accept]))
			go func() {
				_ = chromedp.Run(t.ctx, page.HandleJavaScriptDialog(accept))
			}()
		}
	})
}

// requestSent records a new request, or a redirect of one already logged.
func (t *tab) requestSent(ev *network.EventRequestWillBeSent) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextReq++
	t.requests = append(t.requests, &request{
		N: t.nextReq, id: ev.RequestID, Method: ev.Request.Method, URL: ev.Request.URL,
		Type: strings.ToLower(string(ev.Type)), started: time.Now(),
	})
	if len(t.requests) > maxRequests {
		t.requests = t.requests[len(t.requests)-maxRequests:]
	}
}

// update changes the latest logged request with id (a redirect reuses it).
func (t *tab) update(id network.RequestID, fn func(*request)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := len(t.requests) - 1; i >= 0; i-- {
		if t.requests[i].id == id {
			fn(t.requests[i])
			return
		}
	}
}

// Tab returns the calling agent's active tab, starting Chrome and opening
// a tab for it as needed.
func (s *Session) Tab(ctx context.Context) (*tab, error) {
	owner := tools.Owner(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.start(); err != nil {
		return nil, err
	}
	s.syncTabs()
	if s.active[owner] == nil {
		if _, err := s.newTab(owner); err != nil {
			return nil, err
		}
	}
	s.focusOn(s.active[owner]) // the viewer follows the agent at work
	return s.active[owner], nil
}

// Open starts Chrome, if it isn't running, with a tab for the main agent,
// without counting as the agent using it.
func (s *Session) Open(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.start(); err != nil {
		return err
	}
	s.syncTabs()
	if s.active[""] == nil {
		if _, err := s.newTab(""); err != nil {
			return err
		}
	}
	s.changed()
	return nil
}

// newTab opens a blank tab for owner and makes it that agent's active one.
func (s *Session) newTab(owner string) (*tab, error) {
	ctx, cancel := chromedp.NewContext(s.root)
	t := &tab{ctx: ctx, cancel: cancel, owner: owner}
	s.watch(t)
	if err := chromedp.Run(ctx, s.guard()); err != nil {
		cancel()
		return nil, err
	}
	t.id = chromedp.FromContext(ctx).Target.TargetID
	s.tabs = append(s.tabs, t)
	s.active[owner] = t
	s.changed()
	return t, nil
}

// own returns owner's tabs, in the order they were opened.
func (s *Session) own(owner string) []*tab {
	var out []*tab
	for _, t := range s.tabs {
		if t.owner == owner {
			out = append(out, t)
		}
	}
	return out
}

// resolveTabOwners inherits ownership through the complete opener graph, not
// the order GetTargets happens to return. Known ownership (including the main
// agent's empty owner) is authoritative. Missing openers and cycles without a
// known owner fall back to the main agent.
func resolveTabOwners(infos []*target.Info, known map[target.ID]string) map[target.ID]string {
	owners := make(map[target.ID]string, len(infos)+len(known))
	for id, owner := range known {
		owners[id] = owner
	}
	openers := make(map[target.ID]target.ID, len(infos))
	for _, info := range infos {
		openers[info.TargetID] = info.OpenerID
	}
	for _, info := range infos {
		id := info.TargetID
		var path []target.ID
		seen := map[target.ID]bool{}
		owner := ""
		for id != "" {
			if knownOwner, ok := owners[id]; ok {
				owner = knownOwner
				break
			}
			if seen[id] {
				break
			}
			seen[id] = true
			path = append(path, id)
			id = openers[id]
		}
		for _, id := range path {
			owners[id] = owner
		}
	}
	return owners
}

// syncTabs drops tabs the user closed and adopts ones a page opened
// (target=_blank links, window.open). A new tab belongs to the agent whose
// tab opened it (the main agent when nobody's did) and becomes that agent's
// active tab, which is what following a link that opens one should do.
func (s *Session) syncTabs() {
	ctx, cancel := context.WithTimeout(s.root, 5*time.Second)
	defer cancel()
	infos, err := chromedp.Targets(ctx)
	if err != nil {
		return
	}
	live := map[target.ID]bool{}
	for _, info := range infos {
		if info.Type == "page" {
			live[info.TargetID] = true
		}
	}
	kept := s.tabs[:0]
	owners := map[target.ID]string{}
	for _, t := range s.tabs {
		// A closed opener can still identify its surviving popup's owner.
		owners[t.id] = t.owner
		if live[t.id] {
			kept = append(kept, t)
			continue
		}
		if t.cancel != nil {
			t.cancel()
		}
		if s.active[t.owner] == t {
			delete(s.active, t.owner)
		}
	}
	s.tabs = kept
	resolved := resolveTabOwners(infos, owners)
	for _, info := range infos {
		if _, known := owners[info.TargetID]; known || info.Type != "page" || strings.HasPrefix(info.URL, "devtools://") {
			continue
		}
		tabCtx, cancel := chromedp.NewContext(s.root, chromedp.WithTargetID(info.TargetID))
		t := &tab{id: info.TargetID, ctx: tabCtx, cancel: cancel, owner: resolved[info.TargetID]}
		s.watch(t)
		// A target may disappear between GetTargets and attachTarget, so
		// attaching is bounded. The first Run attaches with the context it is
		// given and keeps the tab's message loop on it, so it must be the
		// tab's own: a short-lived child would stop the loop when it ends,
		// and every later command on the tab would wait forever. The bound
		// cancels the tab itself instead, and only if attaching overruns.
		stopAttach := time.AfterFunc(5*time.Second, cancel)
		err := chromedp.Run(tabCtx, s.guard())
		if !stopAttach.Stop() && err == nil {
			err = context.DeadlineExceeded
		}
		if err != nil {
			cancel()
			continue
		}
		s.tabs = append(s.tabs, t)
		s.active[t.owner] = t
	}
	// An agent whose active tab went away falls back to its latest one.
	for i := len(s.tabs) - 1; i >= 0; i-- {
		if t := s.tabs[i]; s.active[t.owner] == nil {
			s.active[t.owner] = t
		}
	}
	s.changed()
}

// TabInfo describes a tab for browser_tabs.
type TabInfo struct {
	Index  int
	Title  string
	URL    string
	Active bool
}

// Tabs lists the calling agent's tabs.
func (s *Session) Tabs(ctx context.Context) ([]TabInfo, error) {
	owner := tools.Owner(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.start(); err != nil {
		return nil, err
	}
	s.syncTabs()
	var out []TabInfo
	for i, t := range s.own(owner) {
		var title, url string
		rctx, cancel := runCtx(ctx, t.ctx, 5*time.Second)
		_ = chromedp.Run(rctx, chromedp.Title(&title), chromedp.Location(&url))
		cancel()
		out = append(out, TabInfo{Index: i, Title: title, URL: url, Active: t == s.active[owner]})
	}
	return out, nil
}

// NewTab opens a blank tab for the calling agent and makes it active.
func (s *Session) NewTab(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.start(); err != nil {
		return err
	}
	_, err := s.newTab(tools.Owner(ctx))
	return err
}

// ownTab returns the calling agent's tab i. The caller holds s.mu.
func (s *Session) ownTab(owner string, i int) (*tab, error) {
	if err := s.start(); err != nil {
		return nil, err
	}
	s.syncTabs()
	own := s.own(owner)
	if i < 0 || i >= len(own) {
		return nil, fmt.Errorf("no tab %d (there are %d)", i, len(own))
	}
	return own[i], nil
}

// SelectTab makes the calling agent's tab i active and brings it to the front.
func (s *Session) SelectTab(ctx context.Context, i int) error {
	owner := tools.Owner(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.ownTab(owner, i)
	if err != nil {
		return err
	}
	s.active[owner] = t
	rctx, cancel := runCtx(ctx, t.ctx, 5*time.Second)
	defer cancel()
	return chromedp.Run(rctx, page.BringToFront())
}

// CloseTab closes the calling agent's tab i.
func (s *Session) CloseTab(ctx context.Context, i int) error {
	owner := tools.Owner(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	t, err := s.ownTab(owner, i)
	if err != nil {
		return err
	}
	return s.closeTab(ctx, t)
}

// closeTab closes t and picks its owner another active tab. The caller
// holds s.mu.
func (s *Session) closeTab(ctx context.Context, t *tab) error {
	if t.cancel != nil {
		t.cancel() // cancelling a tab's context closes it
	} else {
		// The first tab's context is the browser's; closing the page keeps
		// Chrome running for the others.
		rctx, cancel := runCtx(ctx, t.ctx, 5*time.Second)
		err := chromedp.Run(rctx, page.Close())
		cancel()
		if err != nil {
			return err
		}
	}
	for i, other := range s.tabs {
		if other == t {
			s.tabs = append(s.tabs[:i:i], s.tabs[i+1:]...)
			break
		}
	}
	if s.active[t.owner] == t {
		delete(s.active, t.owner)
		if own := s.own(t.owner); len(own) > 0 {
			s.active[t.owner] = own[len(own)-1]
		}
	}
	s.changed()
	return nil
}

// Release closes the tabs of an agent that has finished. The main agent's
// tabs stay: the user may want to look at them.
func (s *Session) Release(owner string) {
	if s == nil || owner == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == nil || s.root.Err() != nil {
		return
	}
	for _, t := range s.own(owner) {
		_ = s.closeTab(context.Background(), t)
	}
	delete(s.active, owner)
}

// Console returns and clears the active tab's buffered console output.
func (s *Session) Console(ctx context.Context) ([]string, error) {
	t, err := s.Tab(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.console
	t.console = nil
	return out, nil
}

// Requests returns a copy of the active tab's logged network requests.
func (s *Session) Requests(ctx context.Context) ([]request, error) {
	t, err := s.Tab(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]request, len(t.requests))
	for i, r := range t.requests {
		out[i] = *r
	}
	return out, nil
}

// ResponseBody returns the body of logged request number n, as text. The
// browser keeps bodies only for a while, and not across navigations.
func (s *Session) ResponseBody(ctx context.Context, n int) (string, error) {
	reqs, err := s.Requests(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range reqs {
		if r.N != n {
			continue
		}
		var body []byte
		err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) (err error) {
			body, err = network.GetResponseBody(r.id).Do(ctx)
			return err
		}))
		if err != nil {
			return "", fmt.Errorf("the body of request %d is no longer available (%v)", n, err)
		}
		return string(body), nil
	}
	return "", fmt.Errorf("no request %d in the log", n)
}

// runCtx derives a context for one action on a tab: it ends with the tool
// call or after timeout, without closing the tab (only cancelling the
// tab's own context does that).
func runCtx(call, tabCtx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(tabCtx, timeout)
	stop := context.AfterFunc(call, cancel)
	return ctx, func() { stop(); cancel() }
}

// run executes actions on the active tab.
func (s *Session) run(ctx context.Context, actions ...chromedp.Action) error {
	t, err := s.Tab(ctx)
	if err != nil {
		return err
	}
	rctx, cancel := runCtx(ctx, t.ctx, actionTimeout)
	defer cancel()
	err = chromedp.Run(rctx, actions...)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return fmt.Errorf("timed out after %s", actionTimeout)
	}
	return err
}
