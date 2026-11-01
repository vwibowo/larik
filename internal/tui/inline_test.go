package tui

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"larik/internal/agent"
	"larik/internal/llm"
)

func pngBlock(t *testing.T, w, h int) llm.Block {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return llm.Block{Type: llm.BlockImage, MediaType: "image/png", Data: base64.StdEncoding.EncodeToString(b.Bytes())}
}

func inlineModel(t *testing.T) *model {
	t.Helper()
	t.Setenv("TMUX", "")
	t.Setenv("CMUX_SOCKET_PATH", "")
	t.Setenv("TERM_PROGRAM", "ghostty")
	m := testModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m
}

// drain runs cmd and returns the messages it ends in, through tea.Sequence's
// unexported slice of commands.
func drain(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if rv := reflect.ValueOf(msg); rv.IsValid() && rv.Kind() == reflect.Slice && rv.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
		var out []tea.Msg
		for i := range rv.Len() {
			c, _ := rv.Index(i).Interface().(tea.Cmd)
			out = append(out, drain(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func runRaw(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	raw, _ := cmd().(tea.RawMsg)
	s, _ := raw.Msg.(string)
	return s
}

func upper(s ...string) string { return strings.ToUpper(strings.Join(s, "")) }

func TestInlineFit(t *testing.T) {
	for _, tc := range []struct {
		name                string
		w, h, width, height int
		want                inlineSize
	}{
		{"a wide shot follows the width in steps", 1600, 800, 61, 40, inlineSize{56, 14}},
		{"capped at the most columns", 4000, 2000, 300, 90, inlineSize{96, 24}},
		{"a tall shot is limited by the window", 800, 5000, 100, 30, inlineSize{6, 20}},
		{"a small picture isn't enlarged", 40, 40, 100, 40, inlineSize{5, 3}},
		{"no room", 800, 400, 0, 40, inlineSize{}},
	} {
		if got := inlineFit(tc.w, tc.h, tc.width, tc.height, 10, 20); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// An image is a few lines of text in the conversation, sent to the terminal
// once for each size, and the wrapping leaves it whole.
func TestToolImagesShowInTheConversation(t *testing.T) {
	m := inlineModel(t)
	for _, msg := range drain(m.showToolImages(agent.Event{Kind: agent.EvToolEnd, Images: []llm.Block{pngBlock(t, 1600, 800)}})) {
		m.Update(msg)
	}
	if len(m.outputs) != 1 {
		t.Fatalf("%d outputs", len(m.outputs))
	}
	o := m.outputs[0]
	if o.lines < 2 || strings.Count(strings.Join(m.convLines, "\n"), "\U0010EEEE") < 40 {
		t.Fatalf("%d lines: %q", o.lines, m.convLines)
	}
	sent := runRaw(m.inline.send())
	if strings.Count(sent, "a=T,U=1") != 1 {
		t.Fatalf("sent %.120q", sent)
	}
	before := len(m.inline.images[0].ids)
	m.rewrapConversationWidth(m.convWidth)
	if len(m.inline.images[0].ids) != before || m.inline.send() != nil {
		t.Fatal("a redraw at the same size sends nothing")
	}
	m.rewrapConversationWidth(40)
	if s := runRaw(m.inline.send()); strings.Count(s, "a=T,U=1") != 1 {
		t.Fatalf("a new size is sent once: %.120q", s)
	}
}

func TestToolImagesNeedATestedTerminal(t *testing.T) {
	m := inlineModel(t)
	t.Setenv("TMUX", "/tmp/tmux,1,0")
	if m.showToolImages(agent.Event{Images: []llm.Block{pngBlock(t, 10, 10)}}) != nil {
		t.Error("tmux keeps the text line")
	}
	t.Setenv("TMUX", "")
	off := false
	m.opts.Config.InlineImages = &off
	if m.showToolImages(agent.Event{Images: []llm.Block{pngBlock(t, 10, 10)}}) != nil {
		t.Error("the setting turns it off")
	}
	m.opts.Config.InlineImages = nil
	if m.showToolImages(agent.Event{Agent: "explore", Images: []llm.Block{pngBlock(t, 10, 10)}}) != nil {
		t.Error("a subagent's images stay out")
	}
}

func TestInlineDropsOldImages(t *testing.T) {
	var s inlineImages
	b := pngBlock(t, 10, 10)
	first := s.add(b)
	s.lines(first, 80, 40, upper)
	for range inlineKeep {
		s.add(b)
	}
	if len(s.images) != inlineKeep || first.data != nil {
		t.Fatalf("%d kept", len(s.images))
	}
	if len(s.deletes) != 1 {
		t.Errorf("the terminal is told: %v", s.deletes)
	}
	if got := s.lines(first, 80, 40, upper); !strings.Contains(got, "NO LONGER HELD") {
		t.Errorf("got %q", got)
	}
}

func TestInlineResetDeletesFromTheTerminal(t *testing.T) {
	var s inlineImages
	s.lines(s.add(pngBlock(t, 10, 10)), 80, 40, upper)
	var out bytes.Buffer
	s.forget(&out)
	if !strings.Contains(out.String(), "a=d,d=I,i=") || len(s.images) != 0 {
		t.Fatalf("%q", out.String())
	}
	if s.add(llm.Block{Type: llm.BlockImage, MediaType: "image/gif", Data: "x"}) != nil {
		t.Error("a format the terminal path can't decode is skipped")
	}
}
