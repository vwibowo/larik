package termbrowser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// The filmstrip keeps a picture of the page after each action that changed
// it, for /replay. It stays in memory, so what a page showed never reaches
// the disk unless the user exports it.

const (
	// filmMaxFrames and filmMaxBytes bound the strip; the oldest go first.
	filmMaxFrames = 200
	filmMaxBytes  = 32 << 20
	filmQuality   = 60
	// filmTimeout is how long a capture may hold up the agent. A tab that
	// isn't painting would wait for ever.
	filmTimeout = 800 * time.Millisecond
	// filmStepLabel limits a frame's label.
	filmStepLabel = 80
	// gifMaxWidth and gifDelay shape an exported GIF; the delay is in
	// hundredths of a second.
	gifMaxWidth = 800
	gifDelay    = 100
)

// filmTools are the tools that change what a page shows.
var filmTools = map[string]bool{
	"browser_navigate": true, "browser_history": true, "browser_click": true,
	"browser_type": true, "browser_select": true, "browser_press_key": true,
	"browser_upload": true, "browser_eval": true, "browser_tabs": true,
}

// Frame is one step of the filmstrip.
type Frame struct {
	JPEG  []byte
	Label string
	URL   string
	Agent string
	At    time.Time
}

type filmstrip struct {
	mu     sync.Mutex
	frames []Frame
	bytes  int
}

func (f *filmstrip) add(fr Frame) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, fr)
	f.bytes += len(fr.JPEG)
	for len(f.frames) > 1 && (len(f.frames) > filmMaxFrames || f.bytes > filmMaxBytes) {
		f.bytes -= len(f.frames[0].JPEG)
		f.frames[0] = Frame{}
		f.frames = f.frames[1:]
	}
}

// Film returns the filmstrip so far, oldest first.
func (s *Session) Film() []Frame {
	s.film.mu.Lock()
	defer s.film.mu.Unlock()
	return append([]Frame(nil), s.film.frames...)
}

// recordStep adds the page as it is now, after a tool's action. It never
// fails the action: a frame that can't be had is left out.
func (s *Session) recordStep(ctx context.Context, label, agent string) {
	cctx, cancel := context.WithTimeout(ctx, filmTimeout)
	defer cancel()
	var data []byte
	var url string
	actions := []chromedp.Action{chromedp.Location(&url), chromedp.ActionFunc(func(ctx context.Context) (err error) {
		data, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatJpeg).WithQuality(filmQuality).Do(ctx)
		return err
	})}
	// A viewer keeps the tab it shows painting, and may be showing another
	// on purpose: only a tab nobody is watching may be brought forward.
	if s.viewers() == 0 {
		actions = append([]chromedp.Action{page.BringToFront()}, actions...)
	}
	if s.run(cctx, actions...) != nil || len(data) == 0 {
		return
	}
	s.film.add(Frame{JPEG: data, Label: stepLabel(label), URL: url, Agent: agent, At: time.Now()})
}

// filmLabel names a step. A script's result can hold anything the page
// does, so it stays out of the label.
func filmLabel(tool, content string) string {
	if tool == "browser_eval" {
		return "Ran a script."
	}
	return content
}

// stepLabel is a tool result's first line, cut to size.
func stepLabel(content string) string {
	line, _, _ := strings.Cut(content, "\n")
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > filmStepLabel {
		line = string(r[:filmStepLabel-1]) + "…"
	}
	return line
}

// replayPage is the scrubber: the frames, a slider, and the arrow keys.
// Everything page-made reaches it as JSON, so none of it can become markup.
const replayPage = `<!doctype html><meta charset=utf-8><title>Replay</title>
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src data:; style-src 'unsafe-inline'; script-src 'unsafe-inline'">
<style>
html,body{margin:0;height:100%%;background:#1c1c1e;color:#eee;font:13px -apple-system,system-ui,sans-serif}
body{display:flex;flex-direction:column}
#shot{flex:1;min-height:0;display:flex;align-items:center;justify-content:center}
img{max-width:100%%;max-height:100%%}
#bar{display:flex;gap:8px;align-items:center;padding:6px 10px;background:#2a2a2d}
#scrub{flex:1}#step{padding:0 10px 6px;background:#2a2a2d;color:#aaa;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
button{background:#444;color:#eee;border:0;border-radius:4px;padding:3px 9px;font:inherit}
</style>
<div id=shot><img id=pic alt=""></div>
<div id=bar><button id=play>Play</button><input id=scrub type=range min=0 value=0><span id=count></span></div>
<div id=step></div>
<script>
const frames = %s;
const show = i => {
	const f = frames[i];
	scrub.value = i;
	pic.src = 'data:image/jpeg;base64,' + f.jpeg;
	count.textContent = (i + 1) + ' / ' + frames.length;
	step.textContent = [f.at, f.agent, f.label, f.url].filter(Boolean).join(' · ');
};
let timer = 0;
const stop = () => { clearInterval(timer); timer = 0; play.textContent = 'Play'; };
const go = i => show(Math.max(0, Math.min(frames.length - 1, i)));
scrub.max = frames.length - 1;
scrub.oninput = () => { stop(); show(+scrub.value); };
play.onclick = () => {
	if (timer) return stop();
	if (+scrub.value >= frames.length - 1) show(0);
	play.textContent = 'Pause';
	timer = setInterval(() => +scrub.value >= frames.length - 1 ? stop() : show(+scrub.value + 1), 1000);
};
addEventListener('keydown', e => {
	if (e.key === 'ArrowLeft') { stop(); go(+scrub.value - 1); }
	else if (e.key === 'ArrowRight') { stop(); go(+scrub.value + 1); }
	else if (e.key === ' ') { e.preventDefault(); play.click(); }
	else return;
	e.preventDefault();
});
show(frames.length - 1);
</script>`

// ShowReplay opens the filmstrip in the picture tab.
func (s *Session) ShowReplay(ctx context.Context) error {
	film := s.Film()
	if len(film) == 0 {
		return errors.New("nothing recorded yet: the filmstrip fills as an agent uses the browser")
	}
	type frame struct {
		JPEG  string `json:"jpeg"`
		Label string `json:"label"`
		URL   string `json:"url"`
		Agent string `json:"agent"`
		At    string `json:"at"`
	}
	frames := make([]frame, len(film))
	for i, f := range film {
		frames[i] = frame{base64.StdEncoding.EncodeToString(f.JPEG), f.Label, f.URL, f.Agent, f.At.Format("15:04:05")}
	}
	data, err := json.Marshal(frames) // escapes < and >
	if err != nil {
		return err
	}
	return s.showPage(ctx, fmt.Sprintf(replayPage, data))
}

// WriteGIF exports the filmstrip, one step a second, and returns how many
// frames it wrote.
func (s *Session) WriteGIF(w io.Writer) (int, error) {
	film := s.Film()
	if len(film) == 0 {
		return 0, errors.New("nothing recorded yet: the filmstrip fills as an agent uses the browser")
	}
	var out gif.GIF
	for _, f := range film {
		img, err := jpeg.Decode(bytes.NewReader(f.JPEG))
		if err != nil {
			continue
		}
		out.Image = append(out.Image, gifFrame(img))
		out.Delay = append(out.Delay, gifDelay)
	}
	if len(out.Image) == 0 {
		return 0, errors.New("no frame could be read")
	}
	return len(out.Image), gif.EncodeAll(w, &out)
}

// gifFrame shrinks img to gifMaxWidth and reduces it to the web palette.
func gifFrame(img image.Image) *image.Paletted {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w > gifMaxWidth {
		w, h = gifMaxWidth, max(h*gifMaxWidth/w, 1)
	}
	small := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			small.Set(x, y, img.At(b.Min.X+x*b.Dx()/w, b.Min.Y+y*b.Dy()/h))
		}
	}
	out := image.NewPaletted(small.Bounds(), palette.Plan9)
	draw.Draw(out, out.Rect, small, image.Point{}, draw.Src)
	return out
}
