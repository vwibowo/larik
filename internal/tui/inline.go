package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"io"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/llm"
	"larik/internal/termgfx"
)

// Images tools return and prompts attach show in the conversation as lines of placeholder
// text (termgfx), so they scroll and clip with it. Larik draws the
// conversation itself in the alternate screen, so the terminal's scrollback
// never holds one. The conversation re-renders on a resize, so an image is
// sent to the terminal once per size it is shown at, when a render first
// asks for that size, and never again.

const (
	// firstInlineID leaves the browser viewer's small image ids alone.
	firstInlineID = 0x100000
	// inlineMaxCols and inlineMaxRows bound an image in the conversation.
	inlineMaxCols, inlineMaxRows = 96, 32
	// inlineColStep is how finely an image follows the width: a window
	// being dragged would otherwise send it again at every column.
	inlineColStep = 8
	// inlineKeep is how many images stay available; older ones show as
	// a line of text.
	inlineKeep = 32
)

type inlineSendMsg struct{}

// inlineSize is an image's size in cells.
type inlineSize struct{ cols, rows int }

// inlineImage is one picture a tool returned.
type inlineImage struct {
	data []byte // PNG or JPEG; nil once dropped
	w, h int
	ids  map[inlineSize]int
}

type inlineSend struct {
	data []byte
	id   int
	size inlineSize
}

// inlineImages is what the terminal has been sent.
type inlineImages struct {
	images  []*inlineImage
	nextID  int
	pending []inlineSend
	deletes []int
	waking  bool
	notify  func(tea.Msg) // the program's Send
}

// inlineOn says whether images show in the conversation here.
func (m *model) inlineOn() bool {
	if c := m.opts.Config; c != nil && !c.InlineImagesOn() {
		return false
	}
	return termgfx.InlineTerminal(os.Getenv)
}

// showImages adds the pictures an event carries to the conversation. A
// subagent's are captioned, since it may run beside others.
func (m *model) showImages(e agent.Event) tea.Cmd {
	if len(e.Images) == 0 || !m.inlineOn() {
		return nil
	}
	m.inline.notify = m.send
	var cmds []tea.Cmd
	for _, b := range e.Images {
		img := m.inline.add(b)
		if img == nil {
			continue
		}
		out := m.renderOutput(64, func(m *model, width int) string {
			out := m.inline.lines(img, width, m.height, m.st.dim.Render)
			if e.Agent != "" {
				out = m.agentLine(e, m.st.dim.Render("image")) + "\n" + out
			}
			return out
		}, nil)
		out.image = img
		cmds = append(cmds, func() tea.Msg { return out })
	}
	return tea.Sequence(cmds...)
}

// openImage shows a picture full size in the browser panel, where it can be
// zoomed, or in the system's viewer when the panel isn't available.
func (m *model) openImage(img *inlineImage) tea.Cmd {
	mediaType := "image/jpeg"
	if termgfx.IsPNG(img.data) {
		mediaType = "image/png"
	}
	external := func() tea.Cmd {
		path, err := saveImage(img.data, mediaType)
		if err != nil {
			return m.println(m.st.err.Render("image: " + err.Error()))
		}
		openBrowser(path)
		return nil
	}
	if m.opts.App == nil || m.opts.App.Browser == nil || !m.panelAllowed() {
		return external()
	}
	init, err := m.prepareBrowserPanel()
	if err != nil {
		return external()
	}
	open, data := m.imageOpen, img.data
	if open == nil {
		open = m.opts.App.Browser.ShowImage
	}
	show := func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := open(ctx, mediaType, data); err != nil {
			return outputMsg(m.st.err.Render("image: " + err.Error()))
		}
		return nil
	}
	return tea.Batch(init, show)
}

// saveImage writes a picture to a temporary file for another program.
func saveImage(data []byte, mediaType string) (string, error) {
	f, err := os.CreateTemp("", "larik-image-*."+strings.TrimPrefix(mediaType, "image/"))
	if err != nil {
		return "", err
	}
	defer f.Close()
	_, err = f.Write(data)
	return f.Name(), err
}

// add keeps a picture block, or returns nil for what can't be drawn.
func (s *inlineImages) add(b llm.Block) *inlineImage {
	if b.Type != llm.BlockImage || (b.MediaType != "image/png" && b.MediaType != "image/jpeg") {
		return nil
	}
	data, err := base64.StdEncoding.DecodeString(b.Data)
	if err != nil {
		return nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width == 0 || cfg.Height == 0 {
		return nil
	}
	img := &inlineImage{data: data, w: cfg.Width, h: cfg.Height, ids: map[inlineSize]int{}}
	s.images = append(s.images, img)
	if len(s.images) > inlineKeep {
		s.drop(s.images[0])
		s.images = s.images[1:]
	}
	return img
}

func (s *inlineImages) drop(img *inlineImage) {
	for _, id := range img.ids {
		s.deletes = append(s.deletes, id)
	}
	img.data, img.ids = nil, nil
}

// lines is the text that shows img in a conversation width wide, asking
// for it to be sent at that size if it hasn't been.
func (s *inlineImages) lines(img *inlineImage, width, height int, dim func(...string) string) string {
	const indent = "  "
	if img.data == nil {
		return dim(indent + "(image no longer held)")
	}
	cellW, cellH, ok := termgfx.CellPixels()
	if !ok {
		cellW, cellH = 10, 20
	}
	size := inlineFit(img.w, img.h, width-len(indent), height, cellW, cellH)
	if size.cols == 0 {
		return dim(indent + "(image: the window is too narrow)")
	}
	id, ok := img.ids[size]
	if !ok {
		s.nextID++
		id = firstInlineID + s.nextID
		img.ids[size] = id
		s.pending = append(s.pending, inlineSend{img.data, id, size})
		s.wake()
	}
	out := termgfx.Placeholders(id, size.cols, size.rows)
	return indent + strings.Join(out, "\n"+indent)
}

// inlineFit picks the cells for a w by h picture: as wide as it may be in
// steps of inlineColStep, no taller than a good part of the window, never
// enlarged, and keeping its shape in cells cellW by cellH pixels.
func inlineFit(w, h, width, height, cellW, cellH int) inlineSize {
	cols := min(width, inlineMaxCols, w/max(cellW, 1)+1)
	if cols >= inlineColStep {
		cols -= cols % inlineColStep
	}
	maxRows := min(inlineMaxRows, termgfx.MaxRows, max(height*2/3, 1))
	rows := (cols*cellW*h + w*cellH/2) / max(w*cellH, 1)
	if rows > maxRows {
		cols = (maxRows*cellH*w + h*cellW/2) / max(h*cellW, 1)
		rows = maxRows
	}
	if cols < 1 || width < 1 {
		return inlineSize{}
	}
	return inlineSize{cols, max(rows, 1)}
}

// wake has the program run send soon: a render can't return a command, and
// nothing else may be coming to carry one.
func (s *inlineImages) wake() {
	if s.waking || s.notify == nil {
		return
	}
	s.waking = true
	notify := s.notify
	go notify(inlineSendMsg{}) // Send waits for the loop renders run on
}

// send writes what is waiting to the terminal, decoding off the UI loop.
// The escapes move no cursor, so the screen around them is undisturbed.
func (s *inlineImages) send() tea.Cmd {
	s.waking = false
	pending, deletes := s.pending, s.deletes
	s.pending, s.deletes = nil, nil
	if len(pending) == 0 && len(deletes) == 0 {
		return nil
	}
	return func() tea.Msg {
		var out bytes.Buffer
		k := inlineKitty(&out)
		for _, id := range deletes {
			k.Delete(id)
		}
		for _, p := range pending {
			if termgfx.IsPNG(p.data) {
				k.ShowVirtualPNG(p.data, p.id, p.size.cols, p.size.rows)
				continue
			}
			f, err := termgfx.Decode(p.data)
			if err != nil {
				continue
			}
			k.ShowVirtual(f, p.id, p.size.cols, p.size.rows)
			termgfx.Release(f)
		}
		return tea.RawMsg{Msg: out.String()}
	}
}

func inlineKitty(w io.Writer) termgfx.Kitty { return termgfx.Kitty{W: w} }

// reset forgets every image, for a conversation that was cleared. The next
// send removes them from the terminal.
func (s *inlineImages) reset() {
	for _, img := range s.images {
		s.drop(img)
	}
	s.images, s.pending = nil, nil
	if len(s.deletes) > 0 {
		s.wake()
	}
}

// forget removes the images from the terminal directly, as the program
// ends and no more commands run.
func (s *inlineImages) forget(w io.Writer) {
	s.reset()
	k := inlineKitty(w)
	for _, id := range s.deletes {
		k.Delete(id)
	}
	s.deletes = nil
}
