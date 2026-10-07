package agent

import "regexp"

// pdfPageObject matches a page object's type entry, which a PDF writes once
// per page. The trailing class excludes "/Pages", the page-tree node.
var pdfPageObject = regexp.MustCompile(`/Type\s*/Page[^s]`)

// pdfPages counts the pages in a PDF well enough to estimate what it will
// cost in context. It reads the page objects directly rather than parsing
// the file, so a PDF that keeps them in a compressed object stream counts
// short — which errs low, the direction the estimates in estimate.go
// deliberately take. 0 means nothing recognizable was found.
func pdfPages(data []byte) int {
	return len(pdfPageObject.FindAll(data, -1))
}
