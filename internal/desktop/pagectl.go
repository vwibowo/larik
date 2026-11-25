package desktop

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo"

	browserpane "larik/internal/desktop/panes/browser"
	"larik/internal/pageoutline"
)

// Driving a browser tab's page, for scripts (larik desktop page) and for
// larik's own tools in a tab (internal/desktoppage): go somewhere, read
// the page as the browser tools outline it, click and type by the refs
// the outline gives, run a script, and take a screenshot. What a click or
// a key does goes through the page's script, so a page that only takes
// what a person does (isTrusted) may ignore it. The request names its
// tab, or goes to the focused tab, or the browser tab focused last; going
// somewhere with none opens one beside the focused pane.

// Page ops.
const (
	PageNavigate   = "navigate"
	PageBack       = "back"
	PageForward    = "forward"
	PageReload     = "reload"
	PageSnapshot   = "snapshot"
	PageClick      = "click"
	PageType       = "type"
	PageEval       = "eval"
	PageScreenshot = "screenshot"
	PageWait       = "wait"
	PageFill       = "fill"
	PagePress      = "press"
	PageSelect     = "select"
	PageScroll     = "scroll"
	PageConsole    = "console"
)

// PageResult is what a page op found: the page's outline after it, what
// eval returned (as JSON), or the screenshot (a PNG).
type PageResult struct {
	Snapshot *pageoutline.Snapshot `json:"snapshot,omitempty"`
	Value    string                `json:"value,omitempty"`
	PNG      []byte                `json:"png,omitempty"`
	Loading  bool                  `json:"loading,omitempty"` // still loading when the wait gave up
}

// pageLoadWait is how long an op waits for the page it leads to.
const pageLoadWait = 15 * time.Second

// pageControl runs a page op, off the main thread but for finding the
// tab: the page answers scripts while the window draws.
func (a *App) pageControl(req Request) Response {
	var (
		t    *tab
		info TabInfo
		msg  string
	)
	if !a.onMain(func() {
		t, msg = a.pageTab(req)
		if t != nil {
			info = a.tabInfo(t)
		}
	}) {
		return Response{Error: "larik desktop is closing"}
	}
	if msg != "" {
		return Response{Error: msg}
	}
	res, err := a.runPage(t, req)
	if err != nil {
		return Response{Error: err.Error(), Tab: &info}
	}
	a.syncPageURL(t, t.browserState().Web.Page().URL())
	a.onMain(func() { info = a.tabInfo(t) })
	return Response{Tab: &info, Page: res}
}

// pageTab is the browser tab req is for, opening one to go somewhere when
// there is none.
func (a *App) pageTab(req Request) (*tab, string) {
	var t *tab
	switch {
	case req.Tab != "":
		var err string
		if t, err = a.tabRef(req.Tab); err != "" {
			return nil, err
		}
		if !t.isBrowser() {
			return nil, "the tab isn't a browser tab"
		}
	case a.focused() != nil && a.focused().isBrowser():
		t = a.focused()
	case a.lastBrowser != nil && a.hasTab(a.lastBrowser):
		t = a.lastBrowser
	case req.Op == PageNavigate:
		u, ok := normalizeURL(req.URL)
		if !ok {
			return nil, fmt.Sprintf("%q isn't a web address", req.URL)
		}
		if t = a.startBrowser(u); t == nil {
			return nil, cmp.Or(a.notice, "couldn't open a browser tab")
		}
		focus := a.focused()
		a.showInSplit(t, false)
		if p := a.paneOf(focus); p != nil {
			a.focusPane(p) // the keyboard stays where it was
		}
	default:
		return nil, "no browser tab is open: go somewhere first (op navigate), or open one with larik desktop open --url"
	}
	if t.browserState().Web == nil {
		return nil, "the browser tab has no page"
	}
	return t, ""
}

// hasTab reports whether t is still open.
func (a *App) hasTab(t *tab) bool {
	for _, o := range a.tabs {
		if o == t {
			return true
		}
	}
	return false
}

// runPage runs req's op on t's page.
func (a *App) runPage(t *tab, req Request) (*PageResult, error) {
	limit := pageLoadWait + 10*time.Second
	if req.Op == PageWait && req.Timeout > 0 {
		limit = min(req.Timeout+5*time.Second, time.Minute+5*time.Second)
	}
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	p := t.browserState().Web.Page()
	settle := func() (*PageResult, error) {
		res := &PageResult{Loading: !waitLoad(ctx, p)}
		snap, err := pageSnapshot(ctx, p, "")
		res.Snapshot = snap
		a.syncPageURL(t, snap.URL)
		return res, err
	}
	switch req.Op {
	case PageNavigate:
		u, ok := normalizeURL(req.URL)
		if !ok {
			return nil, fmt.Errorf("%q isn't a web address", req.URL)
		}
		a.update(func() { t.browserState().Address, t.browserState().URL = u, u })
		if err := p.LoadURL(u); err != nil {
			return nil, err
		}
		return settle()
	case PageBack:
		if !p.CanGoBack() {
			return nil, errors.New("the page has nothing to go back to")
		}
		p.GoBack()
		return settle()
	case PageForward:
		if !p.CanGoForward() {
			return nil, errors.New("the page has nothing to go forward to")
		}
		p.GoForward()
		return settle()
	case PageReload:
		p.Reload()
		return settle()
	case PageSnapshot:
		waitLoad(ctx, p)
		snap, err := pageSnapshot(ctx, p, req.Ref)
		a.syncPageURL(t, snap.URL)
		return &PageResult{Snapshot: snap}, err
	case PageClick:
		if err := act(ctx, p, clickScript(req.Ref)); err != nil {
			return nil, err
		}
		return settle()
	case PageType:
		if err := act(ctx, p, typeScript(req.Ref, req.Text, req.Enter)); err != nil {
			return nil, err
		}
		return settle()
	case PageEval:
		v, err := p.EvalContext(ctx, req.Text)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		return &PageResult{Value: string(b)}, nil
	case PageScreenshot:
		waitLoad(ctx, p)
		png, err := t.browserState().Web.CapturePage()
		if err != nil {
			return nil, err
		}
		return &PageResult{PNG: png}, nil
	case PageWait:
		if err := waitPageCondition(ctx, p, req); err != nil {
			return nil, err
		}
		return &PageResult{Value: `"ready"`}, nil
	case PageFill:
		if err := act(ctx, p, typeScript(req.Ref, req.Text, false)); err != nil {
			return nil, err
		}
		return settle()
	case PagePress:
		script, err := pagePressScript(req.Ref, req.Text)
		if err != nil {
			return nil, err
		}
		if err := act(ctx, p, script); err != nil {
			return nil, err
		}
		return settle()
	case PageSelect:
		if err := act(ctx, p, pageSelectScript(req.Ref, req.Text)); err != nil {
			return nil, err
		}
		return settle()
	case PageScroll:
		if err := act(ctx, p, pageScrollScript(req.X, req.Y)); err != nil {
			return nil, err
		}
		return settle()
	case PageConsole:
		value, err := pageConsoleErrors(ctx, p)
		return &PageResult{Value: value}, err
	}
	return nil, fmt.Errorf("unknown page op %q", req.Op)
}

// syncPageURL keeps tab metadata current when a page changes history without
// a WebView navigation event, as single-page apps often do.
func (a *App) syncPageURL(t *tab, u string) {
	if u == "" || u == t.browserState().URL {
		return
	}
	a.update(func() {
		if t.browserState().Address == t.browserState().URL {
			t.browserState().Address = u
		}
		t.browserState().URL = u
	})
}

func waitLoad(ctx context.Context, p *mygo.Page) bool {
	return browserpane.WaitLoad(ctx, p, pageLoadWait)
}
func pageSnapshot(ctx context.Context, p *mygo.Page, ref string) (*pageoutline.Snapshot, error) {
	return browserpane.PageSnapshot(ctx, p, ref)
}
func act(ctx context.Context, p *mygo.Page, js string) error { return browserpane.Act(ctx, p, js) }
func findRef(ref string) string                              { return browserpane.FindRef(ref) }
func clickScript(ref string) string                          { return browserpane.ClickScript(ref) }
func typeScript(ref, text string, enter bool) string         { return browserpane.TypeScript(ref, text, enter) }
func mustJSON(s string) string                               { return browserpane.MustJSON(s) }
