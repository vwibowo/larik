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
	"path/filepath"
	"regexp"
	"strconv"
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

// extraPages are served at their paths by the test server.
var extraPages = map[string]string{
	// Smooth scrolling: the button is far down, so the click must wait
	// for no animation.
	"/smooth": `<!doctype html><title>Smooth</title><style>html{scroll-behavior:smooth}</style>
<div style="height:4000px"></div><button onclick="document.title='Clicked far'">Far button</button><div style="height:2000px"></div>`,
	// A cookie banner over the whole page.
	"/overlay": `<!doctype html><title>Overlay</title>
<button onclick="document.title='Behind'">Behind</button>
<div style="position:fixed;inset:0;background:rgba(0,0,0,.5)"><button onclick="this.parentNode.remove()">Accept cookies</button></div>`,
	// Asks before leaving once the user has typed.
	"/unload": `<!doctype html><title>Unsaved</title><input aria-label="Draft">
<script>addEventListener('beforeunload', e => { e.preventDefault(); e.returnValue = ''; })</script>`,
	"/console": `<!doctype html><title>Console</title><script>console.log("line1\nline2 \"quoted\"", {n: 1}, 7)</script>`,
	// An image that never finishes loading, so the load event never fires.
	"/hanging": `<!doctype html><title>Hanging</title><p>Usable already</p><img src="/never">`,
	// A resource on the cloud metadata address, which must be blocked.
	"/metadata": `<!doctype html><title>Metadata</title><p>Still here</p><img src="http://169.254.169.254/latest/meta-data">`,
	"/download": `<!doctype html><title>Download</title><a href="/file">Get report</a>`,
	"/upload": `<!doctype html><title>Upload</title>
<label>Resume <input type="file" id="f" onchange="document.title = [...this.files].map(f => f.name).join(',')"></label>
<button onclick="document.getElementById('f').click()">Choose resume</button>`,
	// Content that appears later, as after a slow search.
	"/slow": `<!doctype html><title>Slow</title><h1 id="h">Searching</h1>
<button onclick="setTimeout(() => { document.getElementById('h').textContent = 'Quick result' }, 150); setTimeout(() => { document.body.append('Results ready') }, 1200)">Search</button>`,
}

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
	var hang chan struct{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/":
			_, _ = w.Write([]byte(testPage))
		case "/done":
			_, _ = w.Write([]byte("<title>Done</title><p>Thanks, " + r.URL.Query().Get("name") + " (" + r.URL.Query().Get("plan") + ")</p>"))
		case "/visual":
			_, _ = w.Write([]byte(visualPage))
		case "/never":
			select { // a request that never finishes (until the test does)
			case <-hang:
			case <-r.Context().Done():
			}
		case "/file":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
			_, _ = w.Write([]byte("quarterly numbers"))
		default:
			if page, ok := extraPages[r.URL.Path]; ok {
				_, _ = w.Write([]byte(page))
				return
			}
			_, _ = w.Write([]byte("<title>Other</title><p>Another page</p>"))
		}
	}))
	t.Cleanup(srv.Close)
	hang = make(chan struct{})
	t.Cleanup(func() { close(hang) }) // runs before srv.Close
	// Not t.TempDir: Chrome's helpers can still be writing the profile
	// when the test ends, which fails its cleanup.
	profile, err := os.MkdirTemp("", "larik-browser-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(profile) })
	downloads, err := os.MkdirTemp("", "larik-downloads-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(downloads) })
	s := New(Options{Headless: true, ChromePath: path, ProfileDir: profile, DownloadDir: downloads})
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

func open(t *testing.T, s *Session, srv *httptest.Server, path string) string {
	t.Helper()
	res := NavigateTool{s}.Run(context.Background(), nil, json.RawMessage(`{"url":"`+srv.URL+path+`"}`))
	if res.IsError {
		t.Fatal(res.Content)
	}
	return res.Content
}

func TestClicksLandAndSeeOverlays(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()

	snap := open(t, s, srv, "/smooth")
	if r := (ClickTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, snap, `button "Far button"`)+`"}`)); !strings.Contains(r.Content, "Title: Clicked far") {
		t.Errorf("click on a smooth-scrolling page missed: %s", r.Content)
	}

	snap = open(t, s, srv, "/overlay")
	r := ClickTool{s}.Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, snap, `button "Behind"`)+`"}`))
	if !r.IsError || !strings.Contains(r.Content, "is covered by") || !strings.Contains(r.Content, "Accept cookies") {
		t.Errorf("a covered click should fail and name the cover: %s", r.Content)
	}
	if r := (ClickTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, snap, `button "Accept cookies"`)+`"}`)); r.IsError {
		t.Fatal(r.Content)
	}
	if r := (ClickTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, snap, `button "Behind"`)+`"}`)); !strings.Contains(r.Content, "Title: Behind") {
		t.Errorf("click after dismissing the banner: %s", r.Content)
	}
}

func TestNavigationEdges(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()

	// Leaving a page that asks first.
	snap := open(t, s, srv, "/unload")
	if r := (TypeTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, snap, `textbox "Draft"`)+`","text":"unsaved"}`)); r.IsError {
		t.Fatal(r.Content)
	}
	start := time.Now()
	if got := open(t, s, srv, "/other"); !strings.Contains(got, "Another page") {
		t.Errorf("navigating away from a beforeunload page: %s", got)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Errorf("leaving took %s", d)
	}

	// A page whose load event never fires is usable at once.
	start = time.Now()
	if got := open(t, s, srv, "/hanging"); !strings.Contains(got, "Usable already") {
		t.Errorf("hanging page: %s", got)
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("a hanging page took %s", d)
	}

	// Console arguments are decoded, not JSON-escaped.
	open(t, s, srv, "/console")
	if r := (ConsoleTool{s}).Run(ctx, nil, nil); !strings.Contains(r.Content, "[log] line1\nline2 \"quoted\" {\"n\":1} 7") && !strings.Contains(r.Content, "[log] line1\nline2 \"quoted\" Object 7") {
		t.Errorf("console: %q", r.Content)
	}

	// Every request passes the address check, not just the page's URL.
	if got := open(t, s, srv, "/metadata"); !strings.Contains(got, "Still here") {
		t.Errorf("metadata page: %s", got)
	}
	if r := (ConsoleTool{s}).Run(ctx, nil, nil); !strings.Contains(r.Content, "[blocked] http://169.254.169.254/latest/meta-data") {
		t.Errorf("the metadata request wasn't blocked: %s", r.Content)
	}
}

func TestDownloadAndUpload(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()

	snap := open(t, s, srv, "/download")
	if r := (ClickTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, snap, `link "Get report"`)+`"}`)); r.IsError {
		t.Fatal(r.Content)
	}
	var notes []string
	for deadline := time.Now().Add(10 * time.Second); len(notes) == 0 && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		notes = s.takeNotes()
	}
	want := filepath.Join(s.downloadDir, "report.txt")
	if len(notes) != 1 || !strings.Contains(notes[0], "Downloaded report.txt to "+want) {
		t.Fatalf("download notes: %q", notes)
	}
	if data, err := os.ReadFile(want); err != nil || string(data) != "quarterly numbers" {
		t.Errorf("downloaded file: %q, %v", data, err)
	}

	dir := t.TempDir()
	for _, name := range []string{"a.txt", "b.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := &tools.Env{Cwd: dir}
	snap = open(t, s, srv, "/upload")
	input := refOf(t, snap, `filepicker "Resume"`)
	if r := (UploadTool{s}).Run(ctx, env, json.RawMessage(`{"ref":"`+input+`","paths":["a.txt"]}`)); !strings.Contains(r.Content, "Title: a.txt") {
		t.Errorf("upload to the input: %s", r.Content)
	}
	// Through a button that opens the chooser.
	button := refOf(t, snap, `button "Choose resume"`)
	if r := (UploadTool{s}).Run(ctx, env, json.RawMessage(`{"ref":"`+button+`","paths":["b.txt"]}`)); !strings.Contains(r.Content, "Title: b.txt") {
		t.Errorf("upload through a chooser: %s", r.Content)
	}
	if r := (UploadTool{s}).Run(ctx, env, json.RawMessage(`{"ref":"`+input+`","paths":["a.txt","b.txt"]}`)); !r.IsError || !strings.Contains(r.Content, "takes one file") {
		t.Errorf("two files for a single input: %s", r.Content)
	}
	if r := (UploadTool{s}).Run(ctx, env, json.RawMessage(`{"ref":"`+input+`","paths":["../../etc/passwd"]}`)); !r.IsError {
		t.Errorf("a file outside the working directory was uploaded: %s", r.Content)
	}
	// Clicking a file input yourself opens no native dialog, and says so.
	_ = ClickTool{s}.Run(ctx, nil, json.RawMessage(`{"ref":"`+input+`"}`))
	time.Sleep(300 * time.Millisecond)
	if notes := s.takeNotes(); len(notes) != 1 || !strings.Contains(notes[0], "browser_upload") {
		t.Errorf("file chooser note: %q", notes)
	}
}

func TestWaiting(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()

	snap := open(t, s, srv, "/slow")
	// The click waits for the page to settle: the heading changes 150ms in.
	r := ClickTool{s}.Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, snap, `button "Search"`)+`"}`))
	if !strings.Contains(r.Content, `heading[1] "Quick result"`) {
		t.Errorf("click result should show the settled page: %s", r.Content)
	}
	if r := (WaitForTool{s}).Run(ctx, nil, json.RawMessage(`{"text":"Results ready"}`)); r.IsError || !strings.Contains(r.Content, "Results ready") {
		t.Errorf("wait_for text: %s", r.Content)
	}
	if r := (WaitForTool{s}).Run(ctx, nil, json.RawMessage(`{"text_gone":"Searching"}`)); r.IsError {
		t.Errorf("wait_for text_gone: %s", r.Content)
	}
	start := time.Now()
	if r := (WaitForTool{s}).Run(ctx, nil, json.RawMessage(`{"text":"never","seconds":1}`)); !r.IsError || !strings.Contains(r.Content, "didn't appear") {
		t.Errorf("wait_for timeout: %s", r.Content)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("a 1s wait took %s", d)
	}
}

func TestProfileInUse(t *testing.T) {
	dir := t.TempDir()
	host, _ := os.Hostname()
	lock := filepath.Join(dir, "SingletonLock")
	if err := os.Symlink(host+"-"+strconv.Itoa(os.Getpid()), lock); err != nil {
		t.Skip("no symlinks:", err)
	}
	if !profileInUse(dir) {
		t.Error("a lock held by a live process should count")
	}
	_ = os.Remove(lock)
	_ = os.Symlink(host+"-99999999", lock)
	if profileInUse(dir) {
		t.Error("a lock left by a dead process shouldn't count")
	}
	_ = os.Remove(lock)
	_ = os.Symlink("elsewhere-"+strconv.Itoa(os.Getpid()), lock)
	if profileInUse(dir) {
		t.Error("a lock from another host shouldn't count")
	}

	// A taken profile makes the session fall back to a temporary one.
	path := chrome(t)
	_ = os.Remove(lock)
	_ = os.Symlink(host+"-"+strconv.Itoa(os.Getpid()), lock)
	s := New(Options{Headless: true, ChromePath: path, ProfileDir: dir})
	defer s.Close()
	if _, err := s.Tab(); err != nil {
		t.Fatal(err)
	}
	if notes := s.takeNotes(); len(notes) != 1 || !strings.Contains(notes[0], "temporary profile") {
		t.Errorf("fallback note: %q", notes)
	}
	if s.tempProfile == "" {
		t.Error("no temporary profile")
	}
}
