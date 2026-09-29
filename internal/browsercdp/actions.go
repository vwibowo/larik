package browsercdp

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"larik/internal/web"
)

//go:embed snapshot.js
var snapshotJS string

// Snapshot is the page outline the model reads and acts on.
type Snapshot struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Refs  int    `json:"refs"`
}

// Snapshot outlines the active tab, waiting for a navigation in progress
// to finish first.
func (s *Session) Snapshot(ctx context.Context) (Snapshot, error) {
	var snap Snapshot
	var err error
	// A click can start a navigation that destroys the page mid-evaluation;
	// retry until the new document is there.
	for attempt := 0; attempt < 20; attempt++ {
		s.settle(ctx)
		if err = s.run(ctx, chromedp.Evaluate(snapshotJS, &snap)); err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	return snap, err
}

// settle waits (up to 10s) for the document to finish loading.
func (s *Session) settle(ctx context.Context) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		var state string
		if s.run(ctx, chromedp.Evaluate(`document.readyState`, &state)) == nil && state == "complete" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// act runs an input action on the active tab and, if it starts a
// navigation (a submitted form, a followed link), gives that a moment to
// begin, so the snapshot that follows shows the new page, not the old one.
func (s *Session) act(ctx context.Context, actions ...chromedp.Action) error {
	t, err := s.Tab()
	if err != nil {
		return err
	}
	before := t.navs.Load()
	if err := s.run(ctx, actions...); err != nil {
		return err
	}
	for i := 0; i < 5 && t.navs.Load() == before && ctx.Err() == nil; i++ {
		time.Sleep(60 * time.Millisecond) // also lets scripts react and re-render
	}
	return nil
}

// CheckURL rejects what browser_navigate must not open: anything but
// http(s), and hosts that resolve to link-local or cloud-metadata addresses.
func CheckURL(ctx context.Context, raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("not an absolute URL: %q", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("only http and https URLs can be opened, not %s:", u.Scheme)
	}
	if err := web.CheckHost(ctx, u.Hostname()); err != nil {
		return "", err
	}
	return u.String(), nil
}

// Navigate opens rawURL in the active tab.
func (s *Session) Navigate(ctx context.Context, rawURL string) error {
	u, err := CheckURL(ctx, rawURL)
	if err != nil {
		return err
	}
	return s.run(ctx, chromedp.Navigate(u))
}

// History goes back or forward in the active tab.
// It doesn't wait for a load event (as chromedp.NavigateBack does): pages
// restored from the back/forward cache never fire one.
func (s *Session) History(ctx context.Context, forward bool) error {
	var cur int64
	var entries []*page.NavigationEntry
	if err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) (err error) {
		cur, entries, err = page.GetNavigationHistory().Do(ctx)
		return err
	})); err != nil {
		return err
	}
	i := cur - 1
	if forward {
		i = cur + 1
	}
	if i < 0 || i >= int64(len(entries)) {
		return fmt.Errorf("there is no page to go %s to", map[bool]string{true: "forward", false: "back"}[forward])
	}
	return s.act(ctx, page.NavigateToHistoryEntry(entries[i].ID))
}

// point scrolls the element with ref into view and returns its center.
func (s *Session) point(ctx context.Context, ref string) (x, y float64, err error) {
	var r struct {
		Found, Disabled bool
		X, Y            float64
	}
	js := fmt.Sprintf(`(() => {
		const el = window.__larikFind && window.__larikFind(%q);
		if (!el) return {Found: false};
		el.scrollIntoView({block: 'center', inline: 'center'});
		const b = el.getBoundingClientRect();
		return {Found: true, Disabled: !!el.disabled, X: b.left + b.width / 2, Y: b.top + b.height / 2};
	})()`, ref)
	if err := s.run(ctx, chromedp.Evaluate(js, &r)); err != nil {
		return 0, 0, err
	}
	if !r.Found {
		return 0, 0, errNoRef(ref)
	}
	if r.Disabled {
		return 0, 0, fmt.Errorf("element %s is disabled", ref)
	}
	return r.X, r.Y, nil
}

func errNoRef(ref string) error {
	return fmt.Errorf("no element with ref %s on this page (it may have changed): take a new browser_snapshot", ref)
}

// Click clicks the element with ref, as a real mouse would.
func (s *Session) Click(ctx context.Context, ref string, double bool) error {
	x, y, err := s.point(ctx, ref)
	if err != nil {
		return err
	}
	opts := []chromedp.MouseOption{}
	if double {
		opts = append(opts, chromedp.ClickCount(2))
	}
	return s.act(ctx, chromedp.MouseClickXY(x, y, opts...))
}

// Hover moves the mouse over the element with ref.
func (s *Session) Hover(ctx context.Context, ref string) error {
	x, y, err := s.point(ctx, ref)
	if err != nil {
		return err
	}
	return s.act(ctx, chromedp.MouseEvent(input.MouseMoved, x, y))
}

// Type focuses the element with ref, replaces its content (unless
// appending) and types text key by key, so pages see real input events.
func (s *Session) Type(ctx context.Context, ref, text string, appendText, submit bool) error {
	var found bool
	js := fmt.Sprintf(`(() => {
		const el = window.__larikFind && window.__larikFind(%q);
		if (!el) return false;
		el.scrollIntoView({block: 'center'});
		el.focus();
		if (!%t) {
			if (el.isContentEditable) {
				document.getSelection().selectAllChildren(el);
				document.execCommand('delete');
			} else if ('value' in el) {
				// The native setter, so frameworks that track value notice.
				const proto = Object.getPrototypeOf(el);
				const set = Object.getOwnPropertyDescriptor(proto, 'value')?.set;
				set ? set.call(el, '') : (el.value = '');
				el.dispatchEvent(new Event('input', {bubbles: true}));
			}
		}
		return true;
	})()`, ref, appendText)
	if err := s.run(ctx, chromedp.Evaluate(js, &found)); err != nil {
		return err
	}
	if !found {
		return errNoRef(ref)
	}
	keys := text
	if submit {
		keys += kb.Enter
	}
	return s.act(ctx, chromedp.KeyEvent(keys))
}

// Select chooses options of the <select> with ref, by value or label.
func (s *Session) Select(ctx context.Context, ref string, values []string) (selected []string, err error) {
	want, _ := json.Marshal(values)
	var r struct {
		Error    string
		Selected []string
	}
	js := fmt.Sprintf(`(() => {
		const el = window.__larikFind && window.__larikFind(%q);
		if (!el) return {Error: 'noref'};
		if (el.tagName !== 'SELECT') return {Error: 'element is a ' + el.tagName.toLowerCase() + ', not a select: click it instead'};
		const want = %s;
		const hits = [...el.options].filter(o => want.includes(o.value) || want.includes(o.text.trim()));
		if (!hits.length) return {Error: 'no option matches ' + JSON.stringify(want)};
		if (!el.multiple) hits.length = 1;
		for (const o of el.options) o.selected = hits.includes(o);
		el.dispatchEvent(new Event('input', {bubbles: true}));
		el.dispatchEvent(new Event('change', {bubbles: true}));
		return {Selected: hits.map(o => o.text.trim())};
	})()`, ref, want)
	if err := s.run(ctx, chromedp.Evaluate(js, &r)); err != nil {
		return nil, err
	}
	switch r.Error {
	case "":
		return r.Selected, nil
	case "noref":
		return nil, errNoRef(ref)
	default:
		return nil, fmt.Errorf("%s", r.Error)
	}
}

// keyNames maps the key names the model uses to chromedp's keys.
var keyNames = map[string]string{
	"enter": kb.Enter, "return": kb.Enter, "tab": kb.Tab, "escape": kb.Escape, "esc": kb.Escape,
	"backspace": kb.Backspace, "delete": kb.Delete, "space": " ",
	"arrowup": kb.ArrowUp, "arrowdown": kb.ArrowDown, "arrowleft": kb.ArrowLeft, "arrowright": kb.ArrowRight,
	"up": kb.ArrowUp, "down": kb.ArrowDown, "left": kb.ArrowLeft, "right": kb.ArrowRight,
	"pageup": kb.PageUp, "pagedown": kb.PageDown, "home": kb.Home, "end": kb.End,
}

// PressKey presses a named key (Enter, Tab, Escape, ArrowDown, PageDown…)
// or a single character, on whatever has focus.
func (s *Session) PressKey(ctx context.Context, key string) error {
	k, ok := keyNames[strings.ToLower(key)]
	if !ok {
		if len([]rune(key)) != 1 {
			return fmt.Errorf("unknown key %q: use a single character or Enter, Tab, Escape, Backspace, Delete, Space, ArrowUp/Down/Left/Right, PageUp/PageDown, Home, End", key)
		}
		k = key
	}
	return s.act(ctx, chromedp.KeyEvent(k))
}

// Eval runs a JavaScript expression in the page and returns its result as
// JSON. Promises are awaited.
func (s *Session) Eval(ctx context.Context, js string) (string, error) {
	var res *runtime.RemoteObject
	err := s.run(ctx, chromedp.Evaluate(js, &res, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true).WithReturnByValue(true)
	}))
	if err != nil {
		return "", err
	}
	if res == nil || res.Type == "undefined" {
		return "undefined", nil
	}
	if len(res.Value) > 0 {
		return string(res.Value), nil
	}
	return res.Description, nil
}
