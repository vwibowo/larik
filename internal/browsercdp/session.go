// Package browsercdp drives a Chrome window over the DevTools Protocol for
// the browser_* tools. Chrome starts on first use and stays open for the
// rest of the process; closing its window just makes the next call start
// a new one.
package browsercdp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
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

	"larik/internal/tools"
	"larik/internal/web"
)

// Options configure the browser.
type Options struct {
	Headless   bool
	ChromePath string // empty: find Chrome or Chromium
	ProfileDir string // persistent profile, so sign-ins survive restarts
	// DownloadDir receives files the pages download; empty means a
	// temporary directory.
	DownloadDir string
}

const (
	actionTimeout         = 30 * time.Second
	browserStartupTimeout = 12 * time.Second
	browserProbeTimeout   = 5 * time.Second
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

	// startupTimeout bounds Chrome launch and the initial DevTools guard.
	// The availability probe overrides this with a shorter timeout.
	startupTimeout time.Duration

	// tempProfile is a throwaway profile used when ProfileDir is taken by
	// another Chrome; it's removed on shutdown.
	tempProfile string
	downloadDir string
	downloads   map[string]string // download GUID -> suggested file name

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

// CheckAvailable verifies Chrome can actually start and speak DevTools without
// opening a visible window or touching the user's persistent browser profile.
func CheckAvailable(opts Options) error { return checkAvailable(opts, browserProbeTimeout) }

// checkAvailable is CheckAvailable giving Chrome timeout to start and to
// load the probe page.
func checkAvailable(opts Options, timeout time.Duration) error {
	opts.Headless = true
	opts.ProfileDir = ""
	opts.DownloadDir = ""
	s := New(opts)
	s.startupTimeout = timeout
	defer s.Close()
	if err := s.start(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.root, timeout)
	defer cancel()
	var title string
	if err := chromedp.Run(ctx,
		chromedp.Navigate("data:text/html,<title>larik</title>"),
		chromedp.Title(&title),
	); err != nil {
		return fmt.Errorf("browser probe page failed: %w", err)
	}
	if title != "larik" {
		return fmt.Errorf("browser probe returned unexpected title %q", title)
	}
	return nil
}

// Close quits Chrome, if it was started.
func (s *Session) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdown()
}

func (s *Session) shutdown() {
	if s.cancelRoot != nil {
		s.cancelRoot()
		s.cancelAlloc()
	}
	s.root, s.cancelRoot, s.cancelAlloc = nil, nil, nil
	s.tabs, s.active = nil, nil
	if s.tempProfile != "" {
		_ = os.RemoveAll(s.tempProfile)
		s.tempProfile = ""
	}
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

// launch starts Chrome with profile, or a temporary profile when empty.
func (s *Session) launch(profile string) error {
	if profile == "" {
		dir, err := os.MkdirTemp("", "larik-browser-")
		if err != nil {
			return err
		}
		s.tempProfile, profile = dir, dir
	}
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
	}
	if s.opts.Headless {
		opts = append(opts, chromedp.Headless)
	}
	if s.opts.ChromePath != "" {
		opts = append(opts, chromedp.ExecPath(s.opts.ChromePath))
	}
	opts = append(opts, chromedp.UserDataDir(profile))
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	root, cancelRoot := chromedp.NewContext(allocCtx)
	t := &tab{ctx: root}
	s.watch(t)
	startupTimeout := s.startupTimeout
	if startupTimeout <= 0 {
		startupTimeout = browserStartupTimeout
	}
	// The first Run allocates Chrome using the context passed to it. Running it
	// with a short-lived child context would kill Chrome as soon as that
	// context is canceled, even after a successful launch. Bound startup by
	// canceling the long-lived root only if the deadline expires.
	stopStartupTimer := time.AfterFunc(startupTimeout, cancelRoot)
	err := chromedp.Run(root, s.guard())
	if !stopStartupTimer.Stop() && err != nil {
		// The timer canceled root, which chromedp reports as a cancel;
		// say what happened instead.
		err = fmt.Errorf("no response within %s: %w", startupTimeout, context.DeadlineExceeded)
	}
	if err != nil {
		cancelRoot()
		cancelAlloc()
		if s.tempProfile != "" {
			_ = os.RemoveAll(s.tempProfile)
			s.tempProfile = ""
		}
		return fmt.Errorf("starting Chrome: %w (install Google Chrome or Chromium, or set browser.chrome_path)", err)
	}
	s.root, s.cancelRoot, s.cancelAlloc = root, cancelRoot, cancelAlloc
	t.id = chromedp.FromContext(root).Target.TargetID
	s.tabs, s.active = []*tab{t}, map[string]*tab{"": t}
	s.setupDownloads()
	return nil
}

// profileInUse reports whether a running Chrome holds the profile. Chrome
// marks it with a SingletonLock symlink to "host-pid" (macOS and Linux).
func profileInUse(dir string) bool {
	link, err := os.Readlink(filepath.Join(dir, "SingletonLock"))
	if err != nil {
		return false
	}
	i := strings.LastIndexByte(link, '-')
	if i < 0 {
		return false
	}
	var pid int
	if _, err := fmt.Sscan(link[i+1:], &pid); err != nil || pid <= 0 {
		return false
	}
	if host, _ := os.Hostname(); host != "" && link[:i] != host {
		return false // locked from another machine (a shared home directory)
	}
	return processAlive(pid)
}

// guard turns on request interception for a tab, so every request (not
// just the URL browser_navigate opens) passes the address check.
// It also intercepts file choosers, which would otherwise open a native
// dialog nobody can answer; browser_upload fills them instead.
func (s *Session) guard() chromedp.Action {
	return chromedp.Tasks{
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}}),
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
			t.log("[exception] " + firstLine(msg))
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
	return s.active[owner], nil
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
		if live[t.id] {
			kept = append(kept, t)
			owners[t.id] = t.owner
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
	for _, info := range infos {
		if _, known := owners[info.TargetID]; known || info.Type != "page" || strings.HasPrefix(info.URL, "devtools://") {
			continue
		}
		ctx, cancel := chromedp.NewContext(s.root, chromedp.WithTargetID(info.TargetID))
		t := &tab{id: info.TargetID, ctx: ctx, cancel: cancel, owner: owners[info.OpenerID]}
		s.watch(t)
		if chromedp.Run(ctx, s.guard()) != nil {
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

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
