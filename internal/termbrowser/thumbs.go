package termbrowser

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// A small picture of the page before each turn's first edit shows what
// /rewind would go back to. They stay in memory, like the filmstrip, so
// prompts from an earlier run have none.

const (
	thumbMax     = 200 // the oldest go first
	thumbWidth   = 320
	thumbQuality = 60
	// sheetCols, sheetGap and sheetLabel shape the picture of several: the
	// label's height is sheetDigit rows of 5.
	sheetCols  = 3
	sheetGap   = 8
	sheetDigit = 3
	sheetLabel = 5*sheetDigit + 2*sheetGap
)

// Thumb is a page before a turn's edits.
type Thumb struct {
	JPEG []byte
	At   time.Time
}

type thumbs struct {
	mu   sync.Mutex
	list []Thumb
	due  bool // this turn has no picture yet
}

// NewTurn says the next edit is the first of a turn.
func (s *Session) NewTurn() {
	if s == nil {
		return
	}
	s.thumbs.mu.Lock()
	s.thumbs.due = true
	s.thumbs.mu.Unlock()
}

// Thumbs returns the pictures kept, oldest first.
func (s *Session) Thumbs() []Thumb {
	if s == nil {
		return nil
	}
	s.thumbs.mu.Lock()
	defer s.thumbs.mu.Unlock()
	return slices.Clone(s.thumbs.list)
}

// markThumb keeps a picture if the turn has none, of the main agent's page
// if it has one. A page that can't be had leaves it for the next edit.
func (s *Session) markThumb(tabs map[string]*tab) {
	s.thumbs.mu.Lock()
	defer s.thumbs.mu.Unlock()
	if !s.thumbs.due {
		return
	}
	owners := make([]string, 0, len(tabs))
	for owner := range tabs {
		owners = append(owners, owner)
	}
	slices.Sort(owners) // "" comes first
	for _, owner := range owners {
		if data := s.thumbOf(tabs[owner]); data != nil {
			s.thumbs.due = false
			s.thumbs.list = append(s.thumbs.list, Thumb{data, time.Now()})
			if len(s.thumbs.list) > thumbMax {
				s.thumbs.list = s.thumbs.list[1:]
			}
			return
		}
	}
}

func (s *Session) thumbOf(t *tab) []byte {
	ctx, cancel := runCtx(context.Background(), t.ctx, filmTimeout)
	defer cancel()
	var view pageView
	var data []byte
	actions := []chromedp.Action{chromedp.Evaluate(pageViewJS, &view), chromedp.ActionFunc(func(ctx context.Context) (err error) {
		if !strings.Contains(view.URL, "://") || view.W < 1 {
			return nil
		}
		clip := &page.Viewport{X: view.X, Y: view.Y, Width: view.W, Height: view.H, Scale: thumbWidth / view.W / view.DPR}
		data, err = page.CaptureScreenshot().WithFormat(page.CaptureScreenshotFormatJpeg).WithQuality(thumbQuality).WithClip(clip).Do(ctx)
		return err
	})}
	if s.viewers() == 0 { // see recordStep
		actions = append([]chromedp.Action{page.BringToFront()}, actions...)
	}
	if chromedp.Run(ctx, actions...) != nil {
		return nil
	}
	return data
}

// SheetEntry is one picture of a contact sheet, under its label: digits
// and dashes.
type SheetEntry struct {
	Label string
	JPEG  []byte
}

// ContactSheet lays pictures out in rows, each under its label. It returns
// nil if none can be read.
func ContactSheet(entries []SheetEntry) []byte {
	type cell struct {
		label string
		img   image.Image
	}
	var cells []cell
	w, h := 0, 0
	for _, e := range entries {
		img, err := jpeg.Decode(bytes.NewReader(e.JPEG))
		if err != nil {
			continue
		}
		cells = append(cells, cell{e.Label, img})
		w, h = max(w, img.Bounds().Dx()), max(h, img.Bounds().Dy())
	}
	if len(cells) == 0 {
		return nil
	}
	cols := min(len(cells), sheetCols)
	rows := (len(cells) + sheetCols - 1) / sheetCols
	out := image.NewRGBA(image.Rect(0, 0, cols*(w+sheetGap)+sheetGap, rows*(h+sheetLabel)+sheetGap))
	draw.Draw(out, out.Rect, image.NewUniform(color.RGBA{28, 28, 30, 255}), image.Point{}, draw.Src)
	for i, c := range cells {
		x := sheetGap + i%sheetCols*(w+sheetGap)
		y := i / sheetCols * (h + sheetLabel)
		drawLabel(out, x, y+sheetGap, c.label)
		at := image.Rect(x, y+sheetLabel, x+w, y+sheetLabel+h)
		draw.Draw(out, at, c.img, c.img.Bounds().Min, draw.Src)
	}
	var b bytes.Buffer
	if png.Encode(&b, out) != nil {
		return nil
	}
	return b.Bytes()
}

// glyphs are 3 by 5, a row a string of three bits read from the left.
var glyphs = map[rune][5]string{
	'0': {"111", "101", "101", "101", "111"}, '1': {"010", "110", "010", "010", "111"},
	'2': {"111", "001", "111", "100", "111"}, '3': {"111", "001", "111", "001", "111"},
	'4': {"101", "101", "111", "001", "001"}, '5': {"111", "100", "111", "001", "111"},
	'6': {"111", "100", "111", "101", "111"}, '7': {"111", "001", "010", "010", "010"},
	'8': {"111", "101", "111", "101", "111"}, '9': {"111", "101", "111", "001", "111"},
	'-': {"000", "000", "111", "000", "000"},
}

func drawLabel(out *image.RGBA, x, y int, label string) {
	white := image.NewUniform(color.White)
	for _, r := range label {
		for row, bits := range glyphs[r] {
			for col, bit := range bits {
				if bit == '1' {
					p := image.Pt(x+col*sheetDigit, y+row*sheetDigit)
					draw.Draw(out, image.Rectangle{p, p.Add(image.Pt(sheetDigit, sheetDigit))}, white, image.Point{}, draw.Src)
				}
			}
		}
		x += 4 * sheetDigit
	}
}
