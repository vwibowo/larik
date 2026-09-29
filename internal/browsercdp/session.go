// Package browsercdp drives a Chrome window over the DevTools Protocol for
// the browser_* tools. Chrome starts on first use and stays open for the
// rest of the process; closing its window just makes the next call start
// a new one.
package browsercdp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Options configure the browser.
type Options struct {
	Headless   bool
	ChromePath string // empty: find Chrome or Chromium
	ProfileDir string // persistent profile, so sign-ins survive restarts
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
}

type tab struct {
	id     target.ID
	ctx    context.Context
	cancel context.CancelFunc

	// navs counts navigations started, so an action can tell whether it
	// set one off.
	navs atomic.Int64

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
}

// start launches Chrome unless it's running. The contexts derive from
// Background: a tool call's context ending must not close the browser.
func (s *Session) start() error {
	if s.root != nil && s.root.Err() == nil {
		return nil
	}
	s.shutdown() // the window was closed; start over

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
	if s.opts.ProfileDir != "" {
		opts = append(opts, chromedp.UserDataDir(s.opts.ProfileDir))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	root, cancelRoot := chromedp.NewContext(allocCtx)
	if err := chromedp.Run(root); err != nil {
		cancelRoot()
		cancelAlloc()
		return fmt.Errorf("starting Chrome: %w (install Google Chrome or Chromium, or set browser.chrome_path)", err)
	}
	s.root, s.cancelRoot, s.cancelAlloc = root, cancelRoot, cancelAlloc
	t := &tab{id: chromedp.FromContext(root).Target.TargetID, ctx: root}
	s.watch(t)
	s.tabs, s.active = []*tab{t}, t
	return nil
}

// watch buffers a tab's console output and dismisses its dialogs, which
// would otherwise block the page (and every later call) until answered.
func (s *Session) watch(t *tab) {
	chromedp.ListenTarget(t.ctx, func(ev any) {
		switch ev := ev.(type) {
		case *runtime.EventConsoleAPICalled:
			var args []string
			for _, a := range ev.Args {
				if a.Value != nil {
					args = append(args, strings.Trim(string(a.Value), `"`))
				} else {
					args = append(args, a.Description)
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
		case *page.EventJavascriptDialogOpening:
			// alerts are accepted; confirm/prompt/beforeunload are declined.
			accept := ev.Type == page.DialogTypeAlert
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
	if err := chromedp.Run(ctx); err != nil {
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
		if chromedp.Run(ctx) != nil {
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
