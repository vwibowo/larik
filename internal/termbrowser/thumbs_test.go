package termbrowser

import (
	"bytes"
	"image/jpeg"
	"image/png"
	"os"
	"testing"
)

func TestThumbsKeepOnePicturePerTurn(t *testing.T) {
	s, srv := newSession(t)
	open(t, s, srv, "/")
	eval(t, s, comparePage)
	s.MarkBefore()
	if n := len(s.Thumbs()); n != 0 {
		t.Fatalf("%d pictures before any turn began", n)
	}
	s.NewTurn()
	s.MarkBefore()
	eval(t, s, `document.body.style.background = '#c03'; 1`)
	s.MarkBefore() // the same turn
	s.NewTurn()
	s.MarkBefore()
	got := s.Thumbs()
	if len(got) != 2 {
		t.Fatalf("%d pictures, want 2", len(got))
	}
	img, err := jpeg.Decode(bytes.NewReader(got[0].JPEG))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != thumbWidth {
		t.Fatalf("width %d", img.Bounds().Dx())
	}
	if !got[0].At.Before(got[1].At) || bytes.Equal(got[0].JPEG, got[1].JPEG) {
		t.Fatal("the second turn's picture should show the change")
	}

	sheet := ContactSheet([]SheetEntry{{"1-3", got[0].JPEG}, {"4", got[1].JPEG}, {"5", []byte("not a picture")}})
	both, err := png.Decode(bytes.NewReader(sheet))
	if err != nil {
		t.Fatal(err)
	}
	if want := 2*(thumbWidth+sheetGap) + sheetGap; both.Bounds().Dx() != want {
		t.Fatalf("sheet width %d, want %d", both.Bounds().Dx(), want)
	}
	if out := os.Getenv("SHEET_OUT"); out != "" {
		_ = os.WriteFile(out, sheet, 0o644)
	}
}

func TestContactSheetNeedsAPicture(t *testing.T) {
	if ContactSheet(nil) != nil || ContactSheet([]SheetEntry{{"1", []byte("x")}}) != nil {
		t.Fatal("no picture, no sheet")
	}
}

func TestNewTurnWithoutABrowser(t *testing.T) {
	var none *Session
	none.NewTurn()
	if none.Thumbs() != nil {
		t.Fatal("no session, no pictures")
	}
}
