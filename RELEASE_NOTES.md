# Larik v0.34.0

_November 10, 2026 · Previous release: v0.33.0._

Larik's browser automation tools now operate only on visible browser panes in Larik Desktop. Standalone terminal sessions no longer launch Chrome or expose browser controls.

### Changed

- **Desktop browser panes.** Improved browser navigation, focus tracking, and visible status feedback across Browser, Files, and Database panes.
- **Browser automation.** `desktop_page_*` tools remain available to Larik running in a Desktop terminal tab and control the Desktop's native browser panes. Standalone CLI, TUI, headless, and server sessions no longer register `browser_*` tools or start Chrome.
- **Terminal interface.** Removed the `/browser` controls, browser viewer, and terminal browser compare and replay commands.

### Compatibility

- Existing `browser` configuration fields are ignored, so older config files continue to load. Shared trace and snapshot support remains available to Desktop.
- Browser automation now requires Larik Desktop; standalone browser sessions and their chromedp dependency have been removed.
