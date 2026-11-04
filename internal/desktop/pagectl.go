package desktop

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/egoist/mygo"

	"larik/internal/termbrowser"
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
	Snapshot *termbrowser.Snapshot `json:"snapshot,omitempty"`
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
		if !t.browser {
			return nil, "the tab isn't a browser tab"
		}
	case a.focused() != nil && a.focused().browser:
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
	if t.web == nil {
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
	p := t.web.Page()
	settle := func() (*PageResult, error) {
		res := &PageResult{Loading: !waitLoad(ctx, p)}
		snap, err := pageSnapshot(ctx, p, "")
		res.Snapshot = snap
		return res, err
	}
	switch req.Op {
	case PageNavigate:
		u, ok := normalizeURL(req.URL)
		if !ok {
			return nil, fmt.Errorf("%q isn't a web address", req.URL)
		}
		a.update(func() { t.addr, t.url = u, u })
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
		png, err := t.web.CapturePage()
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

// waitLoad waits for what the page started loading, reporting whether it
// finished before the wait gave up.
func waitLoad(ctx context.Context, p *mygo.Page) bool {
	ctx, cancel := context.WithTimeout(ctx, pageLoadWait)
	defer cancel()
	// A navigation a click starts takes a moment to begin.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
		if i >= 2 && !p.IsLoading() {
			return true
		}
	}
}

// pageSnapshot outlines the page, or the element with ref.
func pageSnapshot(ctx context.Context, p *mygo.Page, ref string) (*termbrowser.Snapshot, error) {
	v, err := p.EvalContext(ctx, termbrowser.SnapshotScript(ref))
	if err != nil {
		return nil, fmt.Errorf("couldn't read the page: %w", err)
	}
	b, _ := json.Marshal(v)
	var snap termbrowser.Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("couldn't read the page: %w", err)
	}
	if snap.Error != "" {
		return nil, errors.New(snap.Error)
	}
	return &snap, nil
}

// act runs a script that acts on an element and answers "" or why it
// couldn't.
func act(ctx context.Context, p *mygo.Page, js string) error {
	v, err := p.EvalContext(ctx, js)
	if err != nil {
		return err
	}
	if msg, _ := v.(string); msg != "" {
		return errors.New(msg)
	}
	return nil
}

// findRef is the script finding the element with ref, as the outline
// named it, or answering why not.
func findRef(ref string) string {
	q, _ := json.Marshal(ref)
	return fmt.Sprintf(`const el = window.__larikFind && window.__larikFind(%s);
	if (!el) return "no element with ref " + %s + " on this page (it may have changed): take a new snapshot";
	if (el.disabled) return "element " + %s + " is disabled";
	el.scrollIntoView({block: "center", inline: "center", behavior: "instant"});`, q, q, q)
}

// clickScript clicks the element with ref.
func clickScript(ref string) string {
	return findRef(ref) + `
	for (const type of ["pointerdown", "mousedown", "pointerup", "mouseup"]) {
		el.dispatchEvent(new (type.startsWith("pointer") ? PointerEvent : MouseEvent)(type, {bubbles: true, cancelable: true, view: window}));
	}
	if (el.focus) el.focus();
	el.click();
	return "";`
}

// typeScript types text into the element with ref, replacing what it
// says, and with enter submits it, as Return would.
func typeScript(ref, text string, enter bool) string {
	t, _ := json.Marshal(text)
	return findRef(ref) + fmt.Sprintf(`
	const text = %s;
	if (el.focus) el.focus();
	if (el.isContentEditable) {
		document.execCommand("selectAll");
		document.execCommand("insertText", false, text);
	} else if ("value" in el) {
		const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : el instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
		const set = Object.getOwnPropertyDescriptor(proto, "value");
		if (set && set.set) set.set.call(el, text); else el.value = text;
		el.dispatchEvent(new InputEvent("input", {bubbles: true, data: text, inputType: "insertText"}));
		el.dispatchEvent(new Event("change", {bubbles: true}));
	} else {
		return "element " + %s + " doesn't take text";
	}
	if (%t) {
		const opts = {key: "Enter", code: "Enter", keyCode: 13, which: 13, bubbles: true, cancelable: true};
		const go = el.dispatchEvent(new KeyboardEvent("keydown", opts));
		el.dispatchEvent(new KeyboardEvent("keypress", opts));
		el.dispatchEvent(new KeyboardEvent("keyup", opts));
		if (go && el.form && el.tagName !== "TEXTAREA") {
			if (el.form.requestSubmit) el.form.requestSubmit(); else el.form.submit();
		}
	}
	return "";`, t, mustJSON(ref), enter)
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
