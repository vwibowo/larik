package termbrowser

import (
	"context"
	"encoding/json"
	"image"
	"net"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/chromedp/chromedp"
)

func TestGhostIDsFollowTheOutline(t *testing.T) {
	if ghostFirstID != pickFirstID+4 {
		t.Fatalf("marker ids from %d overlap the outline's from %d", ghostFirstID, pickFirstID)
	}
}

func TestGhostShowsOnTheTabAndGoes(t *testing.T) {
	v := pickViewer(t)
	v.tab = "tab"
	v.state.Cursor = &Cursor{Tab: "other", X: 20, Y: 20}
	if v.syncGhost() != nil {
		t.Fatal("a click on another tab shows nothing")
	}
	v.state.Cursor.Tab = "tab"
	out := rawOf(v.syncGhost())
	if strings.Count(out, "\x1b_Ga=T") != ghostBars || !strings.Contains(out, ",z=1073741824,") {
		t.Fatalf("a box and a dot above every frame: %.160q", out)
	}
	v.state.Cursor = nil
	if out := rawOf(v.syncGhost()); strings.Count(out, "a=d") != ghostBars {
		t.Fatalf("the marker goes: %.160q", out)
	}
	if v.syncGhost() != nil {
		t.Fatal("nothing left to remove")
	}
}

func TestGhostStaysInsideThePage(t *testing.T) {
	v := pickViewer(t)
	v.tab = "tab"
	v.state.Cursor = &Cursor{Tab: "tab", X: 1, Y: 1}
	if out := rawOf(v.syncGhost()); !strings.Contains(out, "\x1b_Ga=T") {
		t.Fatalf("a click in the corner still shows: %.120q", out)
	}
	v.ghost = false
	v.state.Cursor.X = 1e6
	if v.syncGhost() != nil {
		t.Fatal("a point off the viewer's page shows nothing")
	}
}

func TestGhostMarksTheCellAClickThereWouldHit(t *testing.T) {
	v := pickViewer(t)
	m := tea.Mouse{X: 12, Y: 9}
	x, y, ok := v.pagePoint(m)
	if !ok {
		t.Fatal("the cell is on the page")
	}
	center, _ := v.ghostBox(Cursor{X: x, Y: y})
	cell := image.Pt(center.X/v.cellW, center.Y/v.cellH+headerRows)
	if cell != image.Pt(m.X, m.Y) {
		t.Fatalf("marker over cell %v, click was at %d,%d", cell, m.X, m.Y)
	}
}

// watch makes s count one viewer until the test ends.
func watch(t *testing.T, s *Session) {
	t.Helper()
	ch := make(chan struct{}, 1)
	s.view.mu.Lock()
	if s.view.subs == nil {
		s.view.subs = map[chan struct{}]net.Conn{}
	}
	s.view.subs[ch] = nil
	s.view.mu.Unlock()
	t.Cleanup(func() {
		s.view.mu.Lock()
		delete(s.view.subs, ch)
		s.view.mu.Unlock()
	})
}

func TestClickFollowsAnElementThatMovesDuringThePause(t *testing.T) {
	s, srv := newSession(t)
	ctx := context.Background()
	snap := open(t, s, srv, "/shifting")
	watch(t, s)
	time.AfterFunc(ghostDelay/4, func() {
		var moved bool
		if err := s.run(ctx, chromedp.Evaluate(`target.style.top = '200px'; decoy.style.top = '20px'; true`, &moved)); err != nil {
			t.Error(err)
		}
	})
	if r := (ClickTool{s}).Run(ctx, nil, json.RawMessage(`{"ref":"`+refOf(t, snap, `button "Target"`)+`"}`)); r.IsError {
		t.Fatal(r.Content)
	}
	var title string
	if err := s.run(ctx, chromedp.Title(&title)); err != nil {
		t.Fatal(err)
	}
	if title != "right" {
		t.Fatalf("the click landed on %q", title)
	}
}

func TestClickWaitsWithAViewerAndCanBeStopped(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	done, err := s.showClick(ctx, 5, 5)
	if err != nil {
		t.Fatal(err)
	}
	done()
	if s.state().Cursor != nil {
		t.Fatal("no viewer: nothing is announced")
	}

	watch(t, s)

	start := time.Now()
	done, err = s.showClick(ctx, 5, 6)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < ghostDelay {
		t.Fatal("the click didn't wait")
	}
	if c := s.state().Cursor; c == nil || c.Y != 6 {
		t.Fatalf("the click isn't announced: %+v", c)
	}
	done()
	if s.state().Cursor != nil {
		t.Fatal("the marker stays after the click")
	}

	stopped, cancel := context.WithCancel(ctx)
	time.AfterFunc(ghostDelay/4, cancel)
	if _, err := s.showClick(stopped, 5, 7); err == nil {
		t.Fatal("stopping the turn must stop the click")
	}
	if s.state().Cursor != nil {
		t.Fatal("a stopped click leaves no marker")
	}
}
