package termbrowser

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// browser_render turns a diagram the model wrote into a picture. The source
// is the model's, and may repeat what it read somewhere, so it never runs
// where it could reach anything: each render gets a tab in a browser context
// of its own, without the agent's cookies, that fails every request and is
// closed afterwards. Agents and viewers never see it. Mermaid is vendored
// (mermaid.min.js.gz, version in renderMermaidVersion) so diagrams work
// offline and nothing is fetched from a CDN.

const (
	// renderMermaidVersion is the vendored build, from npm with its
	// published integrity checked.
	renderMermaidVersion = "11.17.2"
	// renderScale is device pixels per CSS pixel, for sharp text.
	renderScale = 2
	// renderDefaultWidth, renderMinWidth and renderMaxWidth are the page's
	// width in CSS pixels; a diagram wider than it still fits, shrunk.
	renderDefaultWidth, renderMinWidth, renderMaxWidth = 900, 200, 1600
	// renderMaxHeight cuts a picture, as maxShotHeight cuts a screenshot.
	renderMaxHeight = 3000
	// maxRenderSource limits what a call may ask to draw.
	maxRenderSource = 256 << 10
)

// renderCSP stops a page loading anything, should a request ever get as far
// as the network: Fetch fails them all first.
const renderCSP = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:">`

// renderPage is what a render's source goes into.
const renderPage = `<!doctype html><html><head><meta charset="utf-8">` + renderCSP +
	`<style>html,body{margin:0;background:#fff;color:#111;font-family:-apple-system,"Segoe UI",Roboto,sans-serif}#larik-render{display:inline-block;padding:16px;max-width:calc(100vw - 32px)}#larik-render>svg{max-width:100%%;height:auto;display:block}</style>` +
	`</head><body><div id="larik-render">%s</div></body></html>`

//go:embed mermaid.min.js.gz
var mermaidGz []byte

// mermaidJS unpacks the vendored Mermaid once.
var mermaidJS = sync.OnceValues(func() (string, error) {
	zr, err := gzip.NewReader(bytes.NewReader(mermaidGz))
	if err != nil {
		return "", err
	}
	js, err := io.ReadAll(zr)
	return string(js), err
})

// mermaidRenderJS draws source with Mermaid. Strict mode keeps a diagram's
// labels from carrying markup or scripts.
const mermaidRenderJS = `(async (source) => {
	const m = globalThis.mermaid;
	m.initialize({startOnLoad: false, securityLevel: 'strict', theme: 'default'});
	const {svg} = await m.render('larik-mermaid', source);
	document.getElementById('larik-render').innerHTML = svg;
	return true;
})(%s)`

// RenderOptions says what to draw.
type RenderOptions struct {
	Kind   string `json:"kind"` // "mermaid", "svg" or "html"
	Source string `json:"source"`
	Width  int    `json:"width"` // CSS pixels; 0 is the default
}

// Rendered is a finished picture.
type Rendered struct {
	PNG           []byte
	Width, Height int  // CSS pixels
	Cut           bool // taller than renderMaxHeight
	Started       bool // Chrome wasn't running, and this started it
}

// renderBrowserContext returns the browser context renders run in, making
// it the first time. The caller holds s.mu.
func (s *Session) renderBrowserContext(ctx context.Context) (cdp.BrowserContextID, error) {
	if s.renderContext != "" {
		return s.renderContext, nil
	}
	rctx, cancel := runCtx(ctx, s.root, 10*time.Second)
	defer cancel()
	var id cdp.BrowserContextID
	err := chromedp.Run(rctx, chromedp.ActionFunc(func(ctx context.Context) (err error) {
		exec := cdp.WithExecutor(ctx, chromedp.FromContext(s.root).Browser)
		id, err = target.CreateBrowserContext().WithDisposeOnDetach(false).Do(exec)
		return err
	}))
	if err != nil {
		return "", err
	}
	s.renderContext = id
	return id, nil
}

// Render draws o in a throwaway offline tab and returns it as a PNG.
func (s *Session) Render(ctx context.Context, o RenderOptions) (Rendered, error) {
	var out Rendered
	source := strings.TrimSpace(o.Source)
	switch {
	case source == "":
		return out, errors.New("source is empty")
	case len(source) > maxRenderSource:
		return out, fmt.Errorf("source is limited to %d KB", maxRenderSource>>10)
	case o.Kind != "mermaid" && o.Kind != "svg" && o.Kind != "html":
		return out, fmt.Errorf("kind must be mermaid, svg or html, not %q", o.Kind)
	}
	width := renderDefaultWidth
	if o.Width > 0 {
		width = min(max(o.Width, renderMinWidth), renderMaxWidth)
	}

	s.mu.Lock()
	out.Started = s.root == nil
	if err := s.start(); err != nil {
		s.mu.Unlock()
		return out, err
	}
	context_, err := s.renderBrowserContext(ctx)
	root := s.root
	s.mu.Unlock()
	if err != nil {
		return out, err
	}

	// A browser context with no window yet needs a new one: without it
	// headless Chrome says no browser is open.
	var id target.ID
	create, cancelCreate := runCtx(ctx, root, 10*time.Second)
	err = chromedp.Run(create, chromedp.ActionFunc(func(ctx context.Context) (err error) {
		exec := cdp.WithExecutor(ctx, chromedp.FromContext(root).Browser)
		id, err = target.CreateTarget("about:blank").WithBrowserContextID(context_).WithNewWindow(true).Do(exec)
		return err
	}))
	cancelCreate()
	if err != nil {
		return out, err
	}
	tabCtx, closeTab := chromedp.NewContext(root, chromedp.WithTargetID(id))
	defer closeTab()
	chromedp.ListenTarget(tabCtx, func(ev any) {
		paused, ok := ev.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		exec := cdp.WithExecutor(tabCtx, chromedp.FromContext(tabCtx).Target)
		// Failing a navigation would swap the page for Chrome's error page;
		// "no content" leaves it where it is.
		if paused.ResourceType == network.ResourceTypeDocument {
			go fetch.FulfillRequest(paused.RequestID, 204).Do(exec) //nolint:errcheck // the tab is going away
			return
		}
		go fetch.FailRequest(paused.RequestID, network.ErrorReasonBlockedByClient).Do(exec) //nolint:errcheck // the tab is going away
	})
	rctx, cancel := runCtx(ctx, tabCtx, actionTimeout)
	defer cancel()

	body := source
	if o.Kind == "mermaid" {
		body = ""
	}
	err = chromedp.Run(rctx,
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*", RequestStage: fetch.RequestStageRequest}}),
		emulation.SetDeviceMetricsOverride(int64(width), 800, renderScale, false),
		// Only Mermaid's own code runs; a page or SVG is drawn, not run.
		emulation.SetScriptExecutionDisabled(o.Kind != "mermaid"),
		chromedp.ActionFunc(func(ctx context.Context) error {
			tree, err := page.GetFrameTree().Do(ctx)
			if err != nil {
				return err
			}
			return page.SetDocumentContent(tree.Frame.ID, fmt.Sprintf(renderPage, body)).Do(ctx)
		}),
	)
	if err != nil {
		return out, err
	}
	if o.Kind == "mermaid" {
		if err := drawMermaid(rctx, source); err != nil {
			return out, err
		}
	}

	var box struct{ X, Y, W, H float64 }
	const measure = `(() => {
		const r = document.getElementById('larik-render').getBoundingClientRect();
		return {X: r.left + scrollX, Y: r.top + scrollY, W: r.width, H: r.height};
	})()`
	if err := chromedp.Run(rctx, chromedp.Evaluate(measure, &box)); err != nil {
		return out, err
	}
	if box.W < 1 || box.H < 1 {
		return out, errors.New("it drew nothing")
	}
	out.Width, out.Height = int(box.W), int(box.H)
	if box.H > renderMaxHeight {
		box.H, out.Cut = renderMaxHeight, true
		out.Height = renderMaxHeight
	}
	clip := &page.Viewport{X: box.X, Y: box.Y, Width: box.W, Height: box.H, Scale: 1}
	err = chromedp.Run(rctx, chromedp.ActionFunc(func(ctx context.Context) (err error) {
		out.PNG, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatPng).
			WithClip(clip).WithCaptureBeyondViewport(true).Do(ctx)
		return err
	}))
	return out, err
}

// drawMermaid loads the vendored Mermaid into the tab and draws source. A
// mistake in the diagram comes back as an error the model can fix.
func drawMermaid(ctx context.Context, source string) error {
	js, err := mermaidJS()
	if err != nil {
		return err
	}
	quoted, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return chromedp.Run(ctx,
		chromedp.Evaluate(js, nil),
		chromedp.Evaluate(fmt.Sprintf(mermaidRenderJS, quoted), nil, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
			return p.WithAwaitPromise(true)
		}),
	)
}
