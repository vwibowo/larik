package termbrowser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"

	"larik/internal/tools"
)

func renderedSize(t *testing.T, data []byte) (int, int) {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img.Bounds().Dx(), img.Bounds().Dy()
}

func TestRenderMermaid(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	if _, err := s.Navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	before := len(s.state().Tabs)
	out, err := s.Render(ctx, RenderOptions{Kind: "mermaid", Source: "graph TD\n  A[Prompt] --> B{Tool?}\n  B -->|yes| C[Run]\n  B -->|no| D[Reply]"})
	if err != nil {
		t.Fatal(err)
	}
	w, h := renderedSize(t, out.PNG)
	if w != out.Width*renderScale || h < 100 || out.Height < 50 {
		t.Fatalf("%dx%d png for %+v", w, h, out.Width)
	}
	s.mu.Lock()
	s.syncTabs()
	s.mu.Unlock()
	if after := len(s.state().Tabs); after != before {
		t.Errorf("tabs %d, was %d: a render's tab must stay hidden", after, before)
	}

	// A mistake comes back for the model to fix.
	if _, err := s.Render(ctx, RenderOptions{Kind: "mermaid", Source: "graph TD\n  A --> --> ["}); err == nil {
		t.Error("a broken diagram must fail")
	}
}

func TestRenderSVGKeepsItsSize(t *testing.T) {
	s, _ := newSession(t)
	out, err := s.Render(context.Background(), RenderOptions{Kind: "svg", Source: `<svg xmlns="http://www.w3.org/2000/svg" width="300" height="100"><rect width="300" height="100" fill="teal"/></svg>`})
	if err != nil {
		t.Fatal(err)
	}
	if out.Width != 332 || out.Height != 132 {
		t.Fatalf("%dx%d", out.Width, out.Height)
	}
}

// Whatever a source tries, nothing leaves the tab.
func TestRenderReachesNothing(t *testing.T) {
	var hits atomic.Int64
	outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer outside.Close()
	s, _ := newSession(t)
	ctx := context.Background()
	page := fmt.Sprintf(`<p>hello</p>
		<img src="%[1]s/img"><link rel="stylesheet" href="%[1]s/css"><iframe src="%[1]s/frame"></iframe>
		<meta http-equiv="refresh" content="0;url=%[1]s/refresh">
		<script>fetch("%[1]s/script"); document.body.innerHTML = "<h1>ran</h1>"</script>
		<img src="x" onerror="fetch('%[1]s/onerror')">`, outside.URL)
	if _, err := s.Render(ctx, RenderOptions{Kind: "html", Source: page}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Render(ctx, RenderOptions{Kind: "mermaid", Source: fmt.Sprintf("graph TD\n  A[\"<img src='%s/mermaid'>\"] --> B", outside.URL)}); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("%d requests got out", n)
	}
}

func TestRenderDoesNotShareTheAgentsCookies(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	if _, err := s.Navigate(ctx, srv.URL); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Eval(ctx, `document.cookie = "session=secret"`); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	id, err := s.renderBrowserContext(ctx)
	root := s.root
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	count := func(in cdp.BrowserContextID) int {
		var found []*network.Cookie
		err := chromedp.Run(root, chromedp.ActionFunc(func(ctx context.Context) (err error) {
			get := storage.GetCookies()
			if in != "" {
				get = get.WithBrowserContextID(in)
			}
			found, err = get.Do(cdp.WithExecutor(ctx, chromedp.FromContext(root).Browser))
			return err
		}))
		if err != nil {
			t.Fatal(err)
		}
		return len(found)
	}
	if count("") == 0 {
		t.Fatal("the agent's cookie should be in its own context")
	}
	if n := count(id); n != 0 {
		t.Errorf("%d cookies in the render context", n)
	}
}

func TestRenderRefusesWhatItCantDraw(t *testing.T) {
	s := New(Options{})
	defer s.Close()
	ctx := context.Background()
	for _, o := range []RenderOptions{
		{Kind: "mermaid"},
		{Kind: "png", Source: "x"},
		{Kind: "svg", Source: strings.Repeat("x", maxRenderSource+1)},
	} {
		if _, err := s.Render(ctx, o); err == nil {
			t.Errorf("%+v should fail", o.Kind)
		}
	}
}

func TestRenderToolSaysWhetherTheUserSawIt(t *testing.T) {
	s, _ := newSession(t)
	input, _ := json.Marshal(RenderOptions{Kind: "svg", Source: `<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"/>`})
	r := (RenderTool{s}).Run(context.Background(), nil, input)
	if r.IsError || len(r.Images) != 1 || !strings.Contains(r.Content, "can't see images") {
		t.Fatalf("%+v", r.Content)
	}
	path := r.Content[strings.Index(r.Content, "saved at ")+len("saved at "):]
	path = strings.TrimSuffix(path, ".")
	defer os.Remove(path)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("fallback file: %v", err)
	}
	s.SetImagesShow(func() bool { return true })
	r = (RenderTool{s}).Run(context.Background(), nil, input)
	if !strings.Contains(r.Content, "can see it") {
		t.Errorf("%q", r.Content)
	}
}

func TestShowImageLoadsThePictureInItsOwnTab(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	open(t, s, srv, "/")
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 30, 20))); err != nil {
		t.Fatal(err)
	}
	if err := s.ShowImage(ctx, "image/png", b.Bytes()); err != nil {
		t.Fatal(err)
	}
	var width int
	owned := tools.WithOwner(ctx, imageTabOwner)
	for range 100 { // the data URL decodes on its own time
		if err := s.run(owned, chromedp.Evaluate(`document.images[0] ? document.images[0].naturalWidth : 0`, &width)); err != nil {
			t.Fatal(err)
		}
		if width != 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if width != 30 {
		t.Fatalf("picture %d wide", width)
	}
	var title string
	if err := s.run(ctx, chromedp.Title(&title)); err != nil {
		t.Fatal(err)
	}
	if title == "Image" {
		t.Fatal("the agent's tab was taken over")
	}
}
