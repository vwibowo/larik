package browsercdp

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
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
	Error string `json:"error"`
}

// Snapshot outlines the active tab, or with ref just that element's
// subtree, waiting for a navigation in progress to finish first.
func (s *Session) Snapshot(ctx context.Context, ref string) (Snapshot, error) {
	var snap Snapshot
	var err error
	js := snapshotJS + "(" + strconv.Quote(ref) + ")"
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	// A click can start a navigation that destroys the page mid-evaluation;
	// retry until the new document is there.
	for attempt := 0; attempt < 20; attempt++ {
		s.settle(ctx)
		if err = s.run(ctx, chromedp.Evaluate(js, &snap)); err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if err == nil && snap.Error == "noref" {
		return snap, errNoRef(ref)
	}
	return snap, err
}

const snapshotTimeout = 20 * time.Second

// settle waits for the document to finish loading: until it's complete,
// or usable ("interactive") for a moment, since pages that keep a request
// open never complete; 10s at most.
func (s *Session) settle(ctx context.Context) {
	start := time.Now()
	for time.Since(start) < 10*time.Second && ctx.Err() == nil {
		var state string
		if s.run(ctx, chromedp.Evaluate(`document.readyState`, &state)) == nil {
			if state == "complete" || (state == "interactive" && time.Since(start) > 1500*time.Millisecond) {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// quietJS resolves once the DOM has gone 300ms without changing (3s at
// most), or the page unloads: scripts reacting to an input, and single-page
// apps changing route, fire no navigation event to wait for.
const quietJS = `new Promise((resolve) => {
	let timer;
	const done = () => { obs.disconnect(); resolve(true); };
	const obs = new MutationObserver(() => { clearTimeout(timer); timer = setTimeout(done, 300); });
	obs.observe(document, {subtree: true, childList: true, attributes: true, characterData: true});
	timer = setTimeout(done, 300);
	setTimeout(done, 3000);
	addEventListener('pagehide', done, {once: true});
})`

// act runs an input action on the active tab and, if it starts a
// navigation (a submitted form, a followed link), gives that a moment to
// begin, so the snapshot that follows shows the new page, not the old one.
func (s *Session) act(ctx context.Context, actions ...chromedp.Action) error {
	if err := s.run(ctx, actions...); err != nil {
		return err
	}
	// If the action started a navigation, the page goes away mid-wait and
	// the evaluation fails; Snapshot then waits for the new document.
	_ = s.run(ctx, chromedp.Evaluate(quietJS, nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	}))
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

// Navigate opens rawURL in the active tab and waits (up to 15s) for the
// new document to be parsed. It doesn't wait for the load event, which pages
// with a hanging request never fire: loading reports such a page, still
// usable, instead of an error.
func (s *Session) Navigate(ctx context.Context, rawURL string) (loading bool, err error) {
	u, err := CheckURL(ctx, rawURL)
	if err != nil {
		return false, err
	}
	t, err := s.Tab(ctx)
	if err != nil {
		return false, err
	}
	before := t.parsed.Load()
	var loader cdp.LoaderID
	var errText string
	var download bool
	if err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) (err error) {
		_, loader, errText, download, err = page.Navigate(u).Do(ctx)
		return err
	})); err != nil {
		return false, err
	}
	if errText != "" && !download {
		return false, fmt.Errorf("could not open %s: %s", u, errText)
	}
	if loader == "" || download {
		// A same-document navigation (a #fragment) loads nothing, and a
		// download leaves the page as it was.
		return false, nil
	}
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		if t.parsed.Load() != before {
			return false, nil
		}
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		time.Sleep(100 * time.Millisecond)
	}
	return true, nil
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
	i := cur - 1 // back
	if forward {
		i = cur + 1
	}
	if i < 0 || i >= int64(len(entries)) {
		return fmt.Errorf("there is no page to go %s to", map[bool]string{true: "forward", false: "back"}[forward])
	}
	return s.act(ctx, page.NavigateToHistoryEntry(entries[i].ID))
}

// point scrolls the element with ref into view and returns its center,
// failing if something else (a banner, a modal) is on top of it there: a
// real click would hit that instead.
func (s *Session) point(ctx context.Context, ref string) (x, y float64, err error) {
	var r struct {
		Found, Disabled bool
		X, Y            float64
		Covered         string
	}
	js := fmt.Sprintf(`(() => {
		const el = window.__larikFind && window.__larikFind(%q);
		if (!el) return {Found: false};
		el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
		const b = el.getBoundingClientRect();
		// An element in an iframe is placed relative to that frame.
		const off = window.__larikOffset(el);
		const x = b.left + b.width / 2 + off.x, y = b.top + b.height / 2 + off.y;
		// What is on top at that point, looking into shadow roots and
		// same-origin frames; lx, ly are the point in the current frame.
		let hit = document.elementFromPoint(x, y), lx = x, ly = y;
		for (let i = 0; hit && i < 20; i++) {
			let inner = null;
			if (hit.shadowRoot) {
				inner = hit.shadowRoot.elementFromPoint(lx, ly);
			} else if (hit.tagName === 'IFRAME' || hit.tagName === 'FRAME') {
				let doc = null;
				try { doc = hit.contentDocument; } catch (e) {}
				if (doc) {
					const r = hit.getBoundingClientRect();
					lx -= r.left + hit.clientLeft;
					ly -= r.top + hit.clientTop;
					inner = doc.elementFromPoint(lx, ly);
				}
			}
			if (!inner || inner === hit) break;
			hit = inner;
		}
		let covered = '';
		if (hit && hit !== el && !el.contains(hit) && hit.control !== el && !(el.labels && [...el.labels].includes(hit))) {
			const owner = hit.closest('[data-larik-ref]');
			const text = (hit.innerText || hit.getAttribute('aria-label') || '').replace(/\s+/g, ' ').trim().slice(0, 60);
			covered = hit.tagName.toLowerCase() + (text ? ' ' + JSON.stringify(text) : '') + (owner ? ' [ref=' + owner.getAttribute('data-larik-ref') + ']' : '');
		}
		return {Found: true, Disabled: !!el.disabled, X: x, Y: y, Covered: covered};
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
	if r.Covered != "" {
		return 0, 0, fmt.Errorf("element %s is covered by %s, which would get the click instead: close or dismiss it first (a cookie banner or dialog, say), or press Escape", ref, r.Covered)
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
		el.scrollIntoView({block: 'center', behavior: 'instant'});
		// Keys go to the focused frame's focused element.
		const doc = el.ownerDocument;
		if (doc.defaultView !== window) doc.defaultView.focus();
		el.focus();
		if (!%t) {
			if (el.isContentEditable) {
				doc.getSelection().selectAllChildren(el);
				doc.execCommand('delete');
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

// WaitFor waits until text appears on the page, or textGone disappears
// (timeout seconds, 30 by default), or with neither just waits seconds
// (at most 10).
func (s *Session) WaitFor(ctx context.Context, text, textGone string, seconds float64) error {
	if text == "" && textGone == "" {
		d := time.Duration(min(max(seconds, 0), 10) * float64(time.Second))
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	timeout := 30 * time.Second
	if seconds > 0 {
		timeout = time.Duration(min(seconds, 60) * float64(time.Second))
	}
	want, gone := text != "", textGone
	if want {
		gone = text
	}
	js := fmt.Sprintf(`(document.body ? document.body.innerText : '').includes(%q)`, gone)
	for deadline := time.Now().Add(timeout); ; {
		var present bool
		if s.run(ctx, chromedp.Evaluate(js, &present)) == nil && present == want {
			return nil
		}
		if time.Now().After(deadline) {
			if want {
				return fmt.Errorf("%q didn't appear within %s", text, timeout)
			}
			return fmt.Errorf("%q was still there after %s", textGone, timeout)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// Upload chooses files for the file input with ref, or for the file
// chooser that clicking ref opens (a styled "Upload" button).
func (s *Session) Upload(ctx context.Context, ref string, paths []string) error {
	var obj *runtime.RemoteObject
	js := fmt.Sprintf(`window.__larikFind && window.__larikFind(%q)`, ref)
	if err := s.run(ctx, chromedp.Evaluate(js, &obj)); err != nil {
		return err
	}
	if obj == nil || obj.ObjectID == "" {
		return errNoRef(ref)
	}
	var info struct{ IsFile, Multiple bool }
	check := `function() { return {IsFile: this.tagName === 'INPUT' && this.type === 'file', Multiple: !!this.multiple}; }`
	if err := s.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		res, exc, err := runtime.CallFunctionOn(check).WithObjectID(obj.ObjectID).WithReturnByValue(true).Do(ctx)
		if err != nil {
			return err
		}
		if exc != nil {
			return exc
		}
		return json.Unmarshal(res.Value, &info)
	})); err != nil {
		return err
	}
	if info.IsFile {
		if len(paths) > 1 && !info.Multiple {
			return fmt.Errorf("the file input %s takes one file, not %d", ref, len(paths))
		}
		return s.act(ctx, dom.SetFileInputFiles(paths).WithObjectID(obj.ObjectID))
	}

	t, err := s.Tab(ctx)
	if err != nil {
		return err
	}
	select { // drop a chooser left from an earlier click
	case <-t.chooser:
	default:
	}
	t.uploading.Store(true)
	defer t.uploading.Store(false)
	if err := s.Click(ctx, ref, false); err != nil {
		return err
	}
	select {
	case ev := <-t.chooser:
		if len(paths) > 1 && ev.Mode != page.FileChooserOpenedModeSelectMultiple {
			return fmt.Errorf("the file chooser takes one file, not %d", len(paths))
		}
		return s.act(ctx, dom.SetFileInputFiles(paths).WithBackendNodeID(ev.BackendNodeID))
	case <-time.After(3 * time.Second):
		return fmt.Errorf("clicking %s didn't open a file chooser: pass the ref of the file input, or of the button that opens one", ref)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// maxShotHeight caps full-page screenshots (CSS pixels); longer pages are
// cut, since models downscale tall images until text is unreadable.
const maxShotHeight = 5000

// ShotOptions choose what Screenshot captures: the viewport by default,
// the whole page, or one element. Labels draws each ref on the page first.
type ShotOptions struct {
	Ref      string
	FullPage bool
	Labels   bool
}

// Shot is a captured screenshot.
type Shot struct {
	JPEG          []byte
	Width, Height int // CSS pixels
	URL           string
	Cut           bool // a full page taller than maxShotHeight
}

// labelsJS draws a ref tag on every element with a ref in the viewport; the
// overlay is removed again after the capture.
const labelsJS = `(() => {
	const layer = document.createElement('div');
	layer.id = '__larik_labels';
	layer.style.cssText = 'position:absolute;left:0;top:0;pointer-events:none;z-index:2147483647';
	const seen = new Set();
	const all = (root) => {
		for (const el of root.querySelectorAll('[data-larik-ref]')) seen.add(el);
		for (const el of root.querySelectorAll('*')) {
			if (el.shadowRoot) all(el.shadowRoot);
			if (el.tagName === 'IFRAME' || el.tagName === 'FRAME') {
				try { if (el.contentDocument) all(el.contentDocument); } catch (e) {}
			}
		}
	};
	all(document);
	for (const el of seen) {
		const b = el.getBoundingClientRect(), off = window.__larikOffset(el);
		const r = {left: b.left + off.x, top: b.top + off.y, width: b.width, height: b.height, bottom: b.bottom + off.y};
		if (!r.width || !r.height || r.bottom < 0 || r.top > innerHeight) continue;
		const tag = document.createElement('div');
		tag.textContent = el.getAttribute('data-larik-ref');
		tag.style.cssText = 'position:absolute;font:bold 11px/1.2 monospace;padding:0 2px;background:#e11d48;color:#fff;border-radius:2px;outline:1px solid #e11d48';
		tag.style.left = (r.left + scrollX) + 'px';
		tag.style.top = Math.max(0, r.top + scrollY - 13) + 'px';
		layer.appendChild(tag);
		const box = document.createElement('div');
		box.style.cssText = 'position:absolute;outline:1px dashed #e11d48';
		Object.assign(box.style, {left: (r.left + scrollX) + 'px', top: (r.top + scrollY) + 'px', width: r.width + 'px', height: r.height + 'px'});
		layer.appendChild(box);
	}
	document.documentElement.appendChild(layer);
	return seen.size;
})()`

// Screenshot captures the active tab as JPEG, at one image pixel per CSS
// pixel whatever the display's scale.
func (s *Session) Screenshot(ctx context.Context, o ShotOptions) (Shot, error) {
	if o.Labels {
		// Labels need refs; a snapshot assigns them (and settles the page).
		if _, err := s.Snapshot(ctx, ""); err != nil {
			return Shot{}, err
		}
		if err := s.run(ctx, chromedp.Evaluate(labelsJS, nil)); err != nil {
			return Shot{}, err
		}
		defer func() {
			_ = s.run(context.WithoutCancel(ctx), chromedp.Evaluate(`document.getElementById('__larik_labels')?.remove()`, nil))
		}()
	} else {
		s.settle(ctx)
	}

	var m struct {
		Found                bool
		DPR, SX, SY, VW, VH  float64
		PW, PH, X, Y, EW, EH float64
		URL                  string
	}
	js := fmt.Sprintf(`(() => {
		const d = document.documentElement;
		const m = {Found: true, DPR: devicePixelRatio || 1, SX: scrollX, SY: scrollY, VW: d.clientWidth || innerWidth, VH: innerHeight,
			PW: Math.max(d.scrollWidth, d.clientWidth), PH: Math.max(d.scrollHeight, document.body ? document.body.scrollHeight : 0), URL: location.href};
		const ref = %q;
		if (ref) {
			const el = window.__larikFind && window.__larikFind(ref);
			if (!el) return {Found: false};
			el.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
			const r = el.getBoundingClientRect();
			const off = window.__larikOffset(el);
			Object.assign(m, {X: r.left + off.x + scrollX, Y: r.top + off.y + scrollY, EW: r.width, EH: r.height});
		}
		return m;
	})()`, o.Ref)
	if err := s.run(ctx, chromedp.Evaluate(js, &m)); err != nil {
		return Shot{}, err
	}
	if !m.Found {
		return Shot{}, errNoRef(o.Ref)
	}

	clip := &page.Viewport{X: m.SX, Y: m.SY, Width: m.VW, Height: m.VH}
	shot := Shot{URL: m.URL}
	switch {
	case o.Ref != "":
		if m.EW < 1 || m.EH < 1 {
			return Shot{}, fmt.Errorf("element %s has no visible area", o.Ref)
		}
		const pad = 8
		x, y := max(m.X-pad, 0), max(m.Y-pad, 0)
		clip = &page.Viewport{X: x, Y: y, Width: min(m.EW+2*pad, m.PW-x), Height: min(m.EH+2*pad, m.PH-y)}
	case o.FullPage:
		h := m.PH
		if h > maxShotHeight {
			h, shot.Cut = maxShotHeight, true
		}
		clip = &page.Viewport{X: 0, Y: 0, Width: m.PW, Height: h}
	}
	clip.Scale = 1 / m.DPR
	// A tab that isn't the visible one may never paint, and the capture
	// would wait for it: bring this agent's tab to the front first.
	err := s.run(ctx, page.BringToFront(), chromedp.ActionFunc(func(ctx context.Context) (err error) {
		shot.JPEG, err = page.CaptureScreenshot().
			WithFormat(page.CaptureScreenshotFormatJpeg).WithQuality(80).
			WithClip(clip).WithCaptureBeyondViewport(o.FullPage).
			Do(ctx)
		return err
	}))
	shot.Width, shot.Height = int(clip.Width), int(clip.Height)
	return shot, err
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
