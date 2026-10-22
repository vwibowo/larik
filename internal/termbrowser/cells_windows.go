package termbrowser

// terminalCellPixels has no pixel size to read on Windows, whose console
// doesn't report one; the viewer then uses its estimate.
func terminalCellPixels() (w, h int, ok bool) { return 0, 0, false }
