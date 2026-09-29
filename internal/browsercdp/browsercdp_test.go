package browsercdp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/emulation"

	"larik/internal/tools"
	"larik/internal/web"
)

const testPage = `<!doctype html>
<title>Test form</title>
<h1>Sign up</h1>
<p>Fill in the form below.</p>
<form action="/done" method="get">
  <label for="name">Name</label><input id="name" name="name">
  <select name="plan"><option value="free">Free</option><option value="pro">Pro</option></select>
  <button type="submit">Send</button>
</form>
<a href="/other">Other page</a>
<button onclick="console.log('clicked', 42); document.title = 'Clicked'">Log</button>
<div style="display:none"><button>Hidden</button></div>
<div id="host"></div>
<script>
  const root = document.getElementById('host').attachShadow({mode: 'open'});
  root.innerHTML = '<button onclick="document.title = \'Shadow\'">In shadow</button>';
</script>`

// visualPage is tall, with a red box to find in screenshots.
const visualPage = `<!doctype html><title>Visual</title>
<style>body{margin:0;background:#fff} #box{position:absolute;left:100px;top:1500px;width:120px;height:80px;background:#f00}</style>
<button style="margin:20px">Go</button>
<div id="box" role="button" tabindex="0" aria-label="Red box"></div>
<div style="height:9000px"></div>`

func chrome(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("starts Chrome")
	}
	for _, p := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("no Chrome or Chromium installed")
	return ""
}

func newSession(t *testing.T) (*Session, *httptest.Server) {
	path := chrome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(testPage))
		case "/done":
			_, _ = w.Write([]byte("<title>Done</title><p>Thanks, " + r.URL.Query().Get("name") + " (" + r.URL.Query().Get("plan") + ")</p>"))
		case "/visual":
			_, _ = w.Write([]byte(visualPage))
		default:
			_, _ = w.Write([]byte("<title>Other</title><p>Another page</p>"))
		}
	}))
	t.Cleanup(srv.Close)
	// Not t.TempDir: Chrome's helpers can still be writing the profile
	// when the test ends, which fails its cleanup.
	profile, err := os.MkdirTemp("", "larik-browser-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(profile) })
	s := New(Options{Headless: true, ChromePath: path, ProfileDir: profile})
	t.Cleanup(s.Close)
	return s, srv
}

// refOf finds the ref of the snapshot line containing needle.
func refOf(t *testing.T, snap, needle string) string {
	t.Helper()
	for _, line := range strings.Split(snap, "\n") {
		if strings.Contains(line, needle) {
			if m := regexp.MustCompile(`\[ref=(e\d+)\]`).FindStringSubmatch(line); m != nil {
				return m[1]
			}
		}
	}
	t.Fatalf("no ref for %q in snapshot:\n%s", needle, snap)
	return ""
}

func TestFormFlow(t *testing.T) {
	s, srv := newSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res := NavigateTool{s}.Run(ctx, nil, json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if res.IsError {
		t.Fatal(res.Content)
	}
	for _, want := range []string{`heading[1] "Sign up"`, `text "Fill in the form below."`, `textbox "Name"`, `combobox`, `*Free`, `button "Send"`, `link "Other page" -> ` + srv.URL + `/other`, `button "In shadow"`, "<web_content"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("snapshot lacks %q:\n%s", want, res.Content)
		}
	}
	if strings.Contains(res.Content, "Hidden") {
		t.Errorf("snapshot shows a hidden element:\n%s", res.Content)
	}

	name := refOf(t, res.Content, `textbox "Name"`)
	if r := (TypeTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+name+`","text":"stale"}`)); r.IsError {
		t.Fatal(r.Content)
	}
	// Typing again replaces the text.
	if r := (TypeTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+name+`","text":"Ada"}`)); r.IsError {
		t.Fatal(r.Content)
	}
	sel := refOf(t, res.Content, "combobox")
	if r := (SelectTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+sel+`","values":["Pro"]}`)); r.IsError || !strings.Contains(r.Content, "*Pro") {
		t.Fatalf("select: %s", r.Content)
	}
	res = ClickTool{s}.Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, res.Content, `button "Send"`)+`"}`))
	if res.IsError || !strings.Contains(res.Content, "Thanks, Ada (pro)") {
		t.Fatalf("after submit: %s", res.Content)
	}

	res = HistoryTool{s}.Run(ctx, nil, json.RawMessage(`{"direction":"back"}`))
	if !strings.Contains(res.Content, "Title: Test form") {
		t.Fatalf("after back: %s", res.Content)
	}
}

func TestClickShadowAndConsole(t *testing.T) {
	s, srv := newSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res := NavigateTool{s}.Run(ctx, nil, json.RawMessage(`{"url":"`+srv.URL+`"}`))
	if res.IsError {
		t.Fatal(res.Content)
	}
	if r := (ClickTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, res.Content, `button "Log"`)+`"}`)); !strings.Contains(r.Content, "Title: Clicked") {
		t.Fatalf("after click: %s", r.Content)
	}
	if r := (ConsoleTool{s}).Run(ctx, nil, nil); !strings.Contains(r.Content, "[log] clicked 42") {
		t.Errorf("console: %s", r.Content)
	}
	if r := (ConsoleTool{s}).Run(ctx, nil, nil); r.Content != "No console messages." {
		t.Errorf("console isn't cleared: %s", r.Content)
	}
	if r := (ClickTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, res.Content, `button "In shadow"`)+`"}`)); !strings.Contains(r.Content, "Title: Shadow") {
		t.Fatalf("shadow click: %s", r.Content)
	}
	if r := (ClickTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"e999"}`)); !r.IsError || !strings.Contains(r.Content, "browser_snapshot") {
		t.Errorf("unknown ref: %+v", r)
	}
	if r := (EvalTool{s}).Run(ctx, nil, json.RawMessage(`{"expression":"Promise.resolve({a: 1 + 1})"}`)); !strings.Contains(r.Content, `{"a":2}`) {
		t.Errorf("eval: %s", r.Content)
	}
}

func TestTabs(t *testing.T) {
	s, srv := newSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	if r := (NavigateTool{s}).Run(ctx, nil, json.RawMessage(`{"url":"`+srv.URL+`"}`)); r.IsError {
		t.Fatal(r.Content)
	}
	if r := (TabsTool{s}).Run(ctx, nil, json.RawMessage(`{"action":"new"}`)); !strings.Contains(r.Content, "* 1.") {
		t.Fatalf("new tab isn't active: %s", r.Content)
	}
	if r := (NavigateTool{s}).Run(ctx, nil, json.RawMessage(`{"url":"`+srv.URL+`/other"}`)); !strings.Contains(r.Content, "Another page") {
		t.Fatal(r.Content)
	}
	// Closing the first tab (the browser's own) keeps the others working.
	r := TabsTool{s}.Run(ctx, nil, json.RawMessage(`{"action":"close","index":0}`))
	if r.IsError || strings.Contains(r.Content, "Test form") || !strings.Contains(r.Content, "* 0. Other") {
		t.Fatalf("after close: %s", r.Content)
	}
	if r := (SnapshotTool{s}).Run(ctx, nil, nil); !strings.Contains(r.Content, "Another page") {
		t.Fatalf("snapshot after close: %s", r.Content)
	}
}

func decode(t *testing.T, r tools.Result) image.Image {
	t.Helper()
	if r.IsError || len(r.Images) != 1 || r.Images[0].MediaType != "image/jpeg" {
		t.Fatalf("want one JPEG: %+v", r.Content)
	}
	data, err := base64.StdEncoding.DecodeString(r.Images[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestScreenshot(t *testing.T) {
	s, srv := newSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	res := NavigateTool{s}.Run(ctx, nil, json.RawMessage(`{"url":"`+srv.URL+`/visual"}`))
	if res.IsError {
		t.Fatal(res.Content)
	}

	// The viewport, in CSS pixels.
	r := ScreenshotTool{s}.Run(ctx, nil, nil)
	if b := decode(t, r).Bounds(); b.Dx() < 1000 || b.Dx() > 1300 || b.Dy() < 500 || b.Dy() > 1000 {
		t.Errorf("viewport screenshot is %v", b)
	}
	if !strings.Contains(r.Content, "the viewport of "+srv.URL+"/visual") {
		t.Errorf("content: %s", r.Content)
	}

	// One element: the red box, below the fold, with padding around it.
	box := refOf(t, res.Content, `button "Red box"`)
	img := decode(t, ScreenshotTool{s}.Run(ctx, nil, json.RawMessage(`{"ref":"`+box+`"}`)))
	if b := img.Bounds(); b.Dx() != 136 || b.Dy() != 96 {
		t.Errorf("element screenshot is %v, want 136×96", b)
	}
	if red, g, b, _ := img.At(68, 48).RGBA(); red>>8 < 200 || g>>8 > 60 || b>>8 > 60 {
		t.Errorf("element screenshot center isn't red: %d %d %d", red>>8, g>>8, b>>8)
	}

	// The full page is cut at maxShotHeight.
	r = ScreenshotTool{s}.Run(ctx, nil, json.RawMessage(`{"full_page":true}`))
	if b := decode(t, r).Bounds(); b.Dy() != maxShotHeight {
		t.Errorf("full page is %v", b)
	}
	if !strings.Contains(r.Content, "cut off") {
		t.Errorf("content should say the page was cut: %s", r.Content)
	}

	// Labels are drawn for the capture and removed afterwards.
	decode(t, ScreenshotTool{s}.Run(ctx, nil, json.RawMessage(`{"labels":true}`)))
	if out, _ := s.Eval(ctx, `!!document.getElementById('__larik_labels')`); out != "false" {
		t.Errorf("label overlay left on the page: %s", out)
	}

	if r := (ScreenshotTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"e999"}`)); !r.IsError {
		t.Error("unknown ref should fail")
	}

	// On a 2x (Retina) display images stay at one pixel per CSS pixel.
	if err := s.run(ctx, emulation.SetDeviceMetricsOverride(1280, 900, 2, false)); err != nil {
		t.Fatal(err)
	}
	if b := decode(t, ScreenshotTool{s}.Run(ctx, nil, json.RawMessage(`{"ref":"`+box+`"}`))).Bounds(); b.Dx() != 136 || b.Dy() != 96 {
		t.Errorf("2x element screenshot is %v, want 136×96", b)
	}
	if b := decode(t, ScreenshotTool{s}.Run(ctx, nil, nil)).Bounds(); b.Dx() != 1280 || b.Dy() != 900 {
		t.Errorf("2x viewport screenshot is %v, want 1280×900", b)
	}
}

func TestCheckURL(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{"file:///etc/passwd", "javascript:alert(1)", "/relative", "chrome://settings"} {
		if _, err := CheckURL(ctx, bad); err == nil {
			t.Errorf("CheckURL(%q) allowed", bad)
		}
	}
	if _, err := CheckURL(ctx, "http://169.254.169.254/latest/meta-data"); !errors.Is(err, web.ErrBlockedAddress) {
		t.Errorf("metadata address: %v", err)
	}
	if _, err := CheckURL(ctx, "http://127.0.0.1:8080/"); err != nil {
		t.Errorf("local dev server: %v", err)
	}
}
