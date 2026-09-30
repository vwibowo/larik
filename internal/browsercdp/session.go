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
	actionTimeout = 30 * time.Second
	maxConsole    = 200
)

// Session is one Chrome process and its tabs.
type Session struct {
	opts Options

	mu          sync.Mutex
	cancelAlloc context.CancelFunc
	root        context.Context // the browser's first tab; parent of the others
	cancelRoot  context.CancelFunc
	tabs        []*tab
	active      *tab

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

	mu      sync.Mutex
	console []string
}

func (t *tab) log(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.console = append(t.console, s)
	if len(t.console) > maxConsole {
		t.console = t.console[len(t.console)-maxConsole:]
	}
}

func New(opts Options) *Session { return &Session{opts: opts} }

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
	if err := chromedp.Run(root, s.guard()); err != nil {
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
	s.tabs, s.active = []*tab{t}, t
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

// Tab returns the active tab, starting Chrome (and opening a tab) as needed.
func (s *Session) Tab() (*tab, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.start(); err != nil {
		return nil, err
	}
	s.syncTabs()
	if s.active == nil {
		if _, err := s.newTab(); err != nil {
			return nil, err
		}
	}
	return s.active, nil
}

// newTab opens a blank tab and makes it active.
func (s *Session) newTab() (*tab, error) {
	ctx, cancel := chromedp.NewContext(s.root)
	t := &tab{ctx: ctx, cancel: cancel}
	s.watch(t)
	if err := chromedp.Run(ctx, s.guard()); err != nil {
		cancel()
		return nil, err
	}
	t.id = chromedp.FromContext(ctx).Target.TargetID
	s.tabs = append(s.tabs, t)
	s.active = t
	return t, nil
}

// syncTabs drops tabs the user closed and adopts ones the page opened
// (target=_blank links, window.open). A new tab becomes active, which is
// what following a link that opens one should do.
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
	known := map[target.ID]bool{}
	for _, t := range s.tabs {
		if live[t.id] {
			kept = append(kept, t)
			known[t.id] = true
		} else {
			if t.cancel != nil {
				t.cancel()
			}
			if s.active == t {
				s.active = nil
			}
		}
	}
	s.tabs = kept
	for _, info := range infos {
		if info.Type != "page" || known[info.TargetID] || strings.HasPrefix(info.URL, "devtools://") {
			continue
		}
		ctx, cancel := chromedp.NewContext(s.root, chromedp.WithTargetID(info.TargetID))
		t := &tab{id: info.TargetID, ctx: ctx, cancel: cancel}
		s.watch(t)
		if chromedp.Run(ctx, s.guard()) != nil {
			cancel()
			continue
		}
		s.tabs = append(s.tabs, t)
		s.active = t
	}
	if s.active == nil && len(s.tabs) > 0 {
		s.active = s.tabs[len(s.tabs)-1]
	}
}

// TabInfo describes a tab for browser_tabs.
type TabInfo struct {
	Index  int
	Title  string
	URL    string
	Active bool
}

// Tabs lists the open tabs.
func (s *Session) Tabs(ctx context.Context) ([]TabInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.start(); err != nil {
		return nil, err
	}
	s.syncTabs()
	var out []TabInfo
	for i, t := range s.tabs {
		var title, url string
		rctx, cancel := runCtx(ctx, t.ctx, 5*time.Second)
		_ = chromedp.Run(rctx, chromedp.Title(&title), chromedp.Location(&url))
		cancel()
		out = append(out, TabInfo{Index: i, Title: title, URL: url, Active: t == s.active})
	}
	return out, nil
}

// NewTab opens a blank tab and makes it active.
func (s *Session) NewTab() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.start(); err != nil {
		return err
	}
	_, err := s.newTab()
	return err
}

// SelectTab makes tab i active and brings it to the front.
func (s *Session) SelectTab(ctx context.Context, i int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.start(); err != nil {
		return err
	}
	s.syncTabs()
	if i < 0 || i >= len(s.tabs) {
		return fmt.Errorf("no tab %d (there are %d)", i, len(s.tabs))
	}
	s.active = s.tabs[i]
	rctx, cancel := runCtx(ctx, s.active.ctx, 5*time.Second)
	defer cancel()
	return chromedp.Run(rctx, page.BringToFront())
}

// CloseTab closes tab i.
func (s *Session) CloseTab(ctx context.Context, i int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.start(); err != nil {
		return err
	}
	s.syncTabs()
	if i < 0 || i >= len(s.tabs) {
		return fmt.Errorf("no tab %d (there are %d)", i, len(s.tabs))
	}
	t := s.tabs[i]
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
	s.tabs = append(s.tabs[:i:i], s.tabs[i+1:]...)
	if s.active == t {
		s.active = nil
		if len(s.tabs) > 0 {
			s.active = s.tabs[len(s.tabs)-1]
		}
	}
	return nil
}

// Console returns and clears the active tab's buffered console output.
func (s *Session) Console() ([]string, error) {
	t, err := s.Tab()
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.console
	t.console = nil
	return out, nil
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
	t, err := s.Tab()
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
