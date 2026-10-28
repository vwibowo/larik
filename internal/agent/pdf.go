package agent

import "larik/internal/tools"

// pdfPages counts a PDF's pages for context estimates; see tools.PDFPages.
func pdfPages(data []byte) int { return tools.PDFPages(data) }
