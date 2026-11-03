package termbrowser

import (
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"strings"
	"testing"

	"larik/internal/tools"
)

const comparePage = `document.body.innerHTML = '<h1 id=title style="margin:40px">Title</h1><p>Some text that stays</p>'; document.body.style.background = '#fff'; 1`

func eval(t *testing.T, s *Session, js string) {
	t.Helper()
	if _, err := s.Eval(context.Background(), js); err != nil {
		t.Fatal(err)
	}
}

func TestComparePageShowsWhatAnEditChanged(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	open(t, s, srv, "/")
	eval(t, s, comparePage)

	if _, err := s.ComparePage(ctx); !errors.Is(err, errNoBefore) {
		t.Fatalf("nothing kept yet: %v", err)
	}
	s.MarkBefore()
	eval(t, s, `title.style.color = '#c03'; 1`)
	s.MarkBefore() // a run of edits keeps the first picture
	eval(t, s, `title.style.background = '#fc0'; 1`)

	c, err := s.ComparePage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.PNG == nil || c.Share <= 0 || c.Share > 0.2 || c.Moving != 0 {
		t.Fatalf("share %v, moving %v", c.Share, c.Moving)
	}
	if !strings.Contains(c.Details, srv.URL) {
		t.Errorf("details: %q", c.Details)
	}
	again, err := s.ComparePage(ctx)
	if err != nil || again.Share != c.Share {
		t.Fatalf("comparing again: %v, %v", again.Share, err)
	}

	// The next edit starts over from the page as it is.
	s.MarkBefore()
	if c, err = s.ComparePage(ctx); err != nil || c.PNG != nil || c.Share != 0 {
		t.Fatalf("no change since the last comparison: %v, %v", c.Share, err)
	}
	if !strings.Contains(c.Details, "No visible change") {
		t.Errorf("details: %q", c.Details)
	}
}

func TestComparePageLeavesOutWhatMovesByItself(t *testing.T) {
	s, srv := newSession(t)
	open(t, s, srv, "/")
	eval(t, s, comparePage)
	eval(t, s, `document.body.insertAdjacentHTML('beforeend', '<style>@keyframes spin{to{transform:rotate(360deg)}}</style><div style="width:80px;height:80px;background:#06c;animation:spin 1s linear infinite"></div>'); 1`)
	s.MarkBefore()
	c, err := s.ComparePage(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.PNG != nil || c.Share != 0 || c.Moving == 0 {
		t.Fatalf("a spinner isn't a change: share %v, moving %v", c.Share, c.Moving)
	}
}

func TestComparePageSurvivesAReloadAndScrolling(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	open(t, s, srv, "/done")
	eval(t, s, `document.body.style.height = '4000px'; scrollTo(0, 300); 1`)
	s.MarkBefore()
	eval(t, s, `scrollTo(0, 900); 1`)
	c, err := s.ComparePage(ctx)
	if err != nil || c.Share != 0 {
		t.Fatalf("scrolling away isn't a change: %v, %v", c.Share, err)
	}
	if _, err := s.Navigate(ctx, srv.URL+"/done"); err != nil { // a dev server's full reload
		t.Fatal(err)
	}
	if c, err = s.ComparePage(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.Details, "scroll position") {
		t.Errorf("a shorter page can't return there: %q", c.Details)
	}
}

func TestComparePageKeepsAgentsApart(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	open(t, s, srv, "/")
	eval(t, s, comparePage)
	s.MarkBefore()
	if err := s.OpenURL(ctx, "other", srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ComparePage(tools.WithOwner(ctx, "other")); !errors.Is(err, errNoBefore) {
		t.Fatalf("the other agent's edits came later: %v", err)
	}
}

func TestBrowserCompareTakesAPage(t *testing.T) {
	s, srv := newSession(t)
	open(t, s, srv, "/")
	eval(t, s, comparePage)
	s.MarkBefore()
	eval(t, s, `title.textContent = 'New title'; 1`)
	res := (CompareTool{s}).Run(context.Background(), nil, json.RawMessage(`{"page":true}`))
	if res.IsError || len(res.Images) != 1 {
		t.Fatalf("%+v", res.Content)
	}
}

func TestMarkBeforeNeverStartsChrome(t *testing.T) {
	var none *Session
	none.MarkBefore()
	s := New(Options{})
	s.MarkBefore()
	if s.root != nil {
		t.Fatal("an edit started Chrome")
	}
}

func TestBeforesKeepsTheLatest(t *testing.T) {
	var b befores
	for _, url := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "a", "i"} {
		b.keep(&pageBefore{url: url})
	}
	if len(b.pages) != beforeKeep || b.find("", "b") != nil || b.find("", "a") == nil {
		t.Fatalf("%d kept", len(b.pages))
	}
}

func TestCompareSteady(t *testing.T) {
	solid := func() *image.RGBA {
		m := image.NewRGBA(image.Rect(0, 0, 80, 80))
		draw.Draw(m, m.Rect, image.NewUniform(color.White), image.Point{}, draw.Src)
		return m
	}
	before, after, again := solid(), solid(), solid()
	after.Set(3, 3, color.Black)   // an edit
	again.Set(3, 3, color.Black)   // and it stays
	after.Set(43, 43, color.Black) // moving: gone in the next picture
	_, share, moving := compareSteady(before, after, again)
	if share != 0.01 || moving != 0.01 {
		t.Fatalf("share %v, moving %v", share, moving)
	}

	// A spinner caught in few places: its moving squares make an L, and
	// before, its corner was inside the L's bounds, away from the L.
	before, after, again = solid(), solid(), solid()
	for i := range 6 {
		after.Set(19, 19+i*8, color.Black) // down the tiles at column 2
		after.Set(19+i*8, 59, color.Black) // and along those at row 7
	}
	before.Set(51, 27, color.Black)
	_, share, moving = compareSteady(before, after, again)
	if share != 0 || moving != 0.12 {
		t.Fatalf("within a moving group's bounds: share %v, moving %v", share, moving)
	}
}
