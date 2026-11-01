package termbrowser

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/gif"
	"image/jpeg"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"larik/internal/tools"
)

func TestFilmDropsTheOldestPastItsLimits(t *testing.T) {
	var f filmstrip
	for i := range filmMaxFrames + 5 {
		f.add(Frame{JPEG: []byte{1}, Label: string(rune(i))})
	}
	if len(f.frames) != filmMaxFrames || f.frames[0].Label != string(rune(5)) {
		t.Fatalf("%d frames, first %q", len(f.frames), f.frames[0].Label)
	}
	f = filmstrip{}
	big := make([]byte, filmMaxBytes/2+1)
	f.add(Frame{JPEG: big})
	f.add(Frame{JPEG: big})
	if len(f.frames) != 1 || f.bytes != len(big) {
		t.Fatalf("%d frames of %d bytes", len(f.frames), f.bytes)
	}
}

func TestFilmLabelsLeaveOutWhatTheyShouldnt(t *testing.T) {
	if got := stepLabel("Clicked e1.\n\nthe page's whole snapshot"); got != "Clicked e1." {
		t.Fatalf("label %q", got)
	}
	if got := stepLabel(strings.Repeat("x", 500)); len([]rune(got)) != filmStepLabel {
		t.Fatalf("%d runes", len([]rune(got)))
	}
	if got := filmLabel("browser_eval", "secret from the page"); strings.Contains(got, "secret") {
		t.Fatalf("label %q", got)
	}
}

func filmRun(t *testing.T, s *Session, tool tools.Tool, input string) string {
	t.Helper()
	r := (noted{tool, s}).Run(context.Background(), nil, json.RawMessage(input))
	if r.IsError {
		t.Fatal(r.Content)
	}
	return r.Content
}

func TestFilmRecordsActionsAndReplays(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	snap := filmRun(t, s, NavigateTool{s}, `{"url":"`+srv.URL+`/"}`)
	filmRun(t, s, SnapshotTool{s}, `{}`) // reading changes nothing
	filmRun(t, s, ClickTool{s}, `{"ref":"`+refOf(t, snap, `button "Log"`)+`"}`)
	film := s.Film()
	if len(film) != 2 {
		t.Fatalf("%d frames, want the navigation and the click", len(film))
	}
	if !strings.HasPrefix(film[1].Label, "Clicked") || !strings.HasPrefix(film[1].URL, srv.URL) {
		t.Fatalf("step: %q at %q", film[1].Label, film[1].URL)
	}
	img, err := jpeg.Decode(bytes.NewReader(film[0].JPEG))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() < 100 {
		t.Fatalf("frame %v", img.Bounds())
	}

	if err := s.ShowReplay(ctx); err != nil {
		t.Fatal(err)
	}
	owned := tools.WithOwner(ctx, imageTabOwner)
	var text string
	for range 100 {
		if err := s.run(owned, chromedp.Evaluate(`count.textContent + ' ' + step.textContent + ' ' + pic.naturalWidth`, &text)); err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(text, " 0") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.HasPrefix(text, "2 / 2 ") || !strings.Contains(text, "Clicked") || strings.HasSuffix(text, " 0") {
		t.Fatalf("the scrubber shows %q", text)
	}
	var back string
	if err := s.run(owned, chromedp.Evaluate(`dispatchEvent(new KeyboardEvent('keydown', {key: 'ArrowLeft'})); count.textContent`, &back)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(back, "1 / 2") {
		t.Fatalf("the left arrow left it at %q", back)
	}

	var out bytes.Buffer
	n, err := s.WriteGIF(&out)
	if err != nil || n != 2 {
		t.Fatalf("%d frames: %v", n, err)
	}
	g, err := gif.DecodeAll(&out)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Image) != 2 || g.Delay[0] != gifDelay || g.Image[0].Bounds().Dx() > gifMaxWidth {
		t.Fatalf("%d frames, delay %d, %v", len(g.Image), g.Delay[0], g.Image[0].Bounds())
	}
}

func TestReplayKeepsPageTextOutOfTheMarkup(t *testing.T) {
	s, _ := newSession(t)
	var pic bytes.Buffer
	if err := jpeg.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil); err != nil {
		t.Fatal(err)
	}
	s.film.add(Frame{JPEG: pic.Bytes(), Label: `</script><img src=x onerror="document.title='owned'">`, At: time.Now()})
	ctx := context.Background()
	if err := s.ShowReplay(ctx); err != nil {
		t.Fatal(err)
	}
	var title, label string
	if err := s.run(tools.WithOwner(ctx, imageTabOwner), chromedp.Title(&title), chromedp.Evaluate(`step.textContent`, &label)); err != nil {
		t.Fatal(err)
	}
	if title == "owned" || !strings.Contains(label, "onerror") {
		t.Fatalf("title %q, label %q", title, label)
	}
}

func TestReplayNeedsSomethingRecorded(t *testing.T) {
	s := &Session{}
	if err := s.ShowReplay(context.Background()); err == nil {
		t.Fatal("an empty filmstrip should say so")
	}
	if _, err := s.WriteGIF(&bytes.Buffer{}); err == nil {
		t.Fatal("an empty filmstrip should say so")
	}
}
