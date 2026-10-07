package agent

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
	"testing"

	"larik/internal/llm"
)

// pdfFixture builds a structurally valid PDF with n page objects, so the
// test owns its fixture rather than depending on a file or a PDF writer.
func pdfFixture(n int) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	var kids []string
	for i := range n {
		kids = append(kids, fmt.Sprintf("%d 0 R", 3+i))
	}
	fmt.Fprintf(&b, "2 0 obj\n<< /Type /Pages /Kids [%s] /Count %d >>\nendobj\n", strings.Join(kids, " "), n)
	for i := range n {
		fmt.Fprintf(&b, "%d 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>\nendobj\n", 3+i)
	}
	fmt.Fprintf(&b, "trailer\n<< /Root 1 0 R /Size %d >>\n%%%%EOF\n", n+3)
	return []byte(b.String())
}

func TestPDFPagesCountsPageObjects(t *testing.T) {
	for _, n := range []int{1, 3, 42} {
		if got := pdfPages(pdfFixture(n)); got != n {
			t.Errorf("pdfPages(%d-page fixture) = %d", n, got)
		}
	}
	// The page-tree node is /Pages and must not be counted as a page; a
	// one-page fixture has exactly one of each.
	one := pdfFixture(1)
	if !bytes.Contains(one, []byte("/Type /Pages")) {
		t.Fatal("fixture should contain the page-tree node")
	}
	if got := pdfPages(one); got != 1 {
		t.Errorf("/Type /Pages was counted as a page: got %d", got)
	}
}

// A PDF that keeps its page objects in a compressed object stream hides
// them from a scan. That must count short rather than wrong, because the
// estimates in estimate.go deliberately err low.
func TestPDFPagesCountsShortWhenObjectsAreCompressed(t *testing.T) {
	var buf bytes.Buffer
	w := zlib.NewWriter(&buf)
	w.Write(pdfFixture(20))
	w.Close()
	hidden := append([]byte("%PDF-1.5\n4 0 obj\n<< /Type /ObjStm >>\nstream\n"), buf.Bytes()...)
	hidden = append(hidden, []byte("\nendstream\nendobj\n%%EOF\n")...)

	got := pdfPages(hidden)
	if got > 0 {
		t.Errorf("compressed page objects should not be found, got %d", got)
	}
	// And the estimate treats an uncounted document as one page, not zero.
	if pageLabel(got) != "unknown" {
		t.Errorf("pageLabel(%d) = %q", got, pageLabel(got))
	}
}

func TestPDFPagesOnRubbish(t *testing.T) {
	for _, b := range [][]byte{nil, []byte(""), []byte("not a pdf at all"), []byte("%PDF-1.4\n")} {
		if got := pdfPages(b); got != 0 {
			t.Errorf("pdfPages(%q) = %d, want 0", b, got)
		}
	}
}

// A document is estimated per page, not by its payload. Measuring the
// base64 as text would overstate a short PDF enormously and compact a
// conversation that still fits — the mistake imageTokens exists to avoid.
func TestDocumentIsEstimatedPerPageNotPerByte(t *testing.T) {
	data := strings.Repeat("A", 400_000) // a payload far larger than its page count
	three := []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{
		{Type: llm.BlockDocument, MediaType: "application/pdf", Data: data, Pages: 3}}}}

	got := estimateMessages(three)
	if want := 3 * documentTokensPerPage; got != want {
		t.Errorf("estimate = %d, want %d (per page, ignoring the payload)", got, want)
	}
	if got > len(data)/charsPerToken/10 {
		t.Errorf("estimate %d is tracking the payload size, not the pages", got)
	}

	// More pages cost proportionally more.
	ten := []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{
		{Type: llm.BlockDocument, Data: "AA==", Pages: 10}}}}
	if estimateMessages(ten) != 10*documentTokensPerPage {
		t.Errorf("ten pages = %d", estimateMessages(ten))
	}

	// An uncounted document still costs a page, never nothing.
	none := []llm.Message{{Role: llm.RoleUser, Blocks: []llm.Block{
		{Type: llm.BlockDocument, Data: "AA==", Pages: 0}}}}
	if estimateMessages(none) != documentTokensPerPage {
		t.Errorf("uncounted document = %d, want one page", estimateMessages(none))
	}
}
