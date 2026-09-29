package tui

import "larik/internal/browser"

// openBrowser opens url in the default browser; tests replace it.
var openBrowser = browser.Open
