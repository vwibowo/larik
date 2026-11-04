package tui

import (
	"context"
	"net/http"
	"time"

	tea "charm.land/bubbletea/v2"

	"larik/internal/release"
)

type versionUpdateMsg struct{ version string }

// checkVersion checks the public latest release without delaying TUI startup.
// A failed check is deliberately silent: version discovery is only a
// convenience and must not make Larik unavailable offline.
func checkVersion(current string) tea.Cmd {
	return func() tea.Msg {
		version, _ := release.Newer(context.Background(), current, release.LatestURL, &http.Client{Timeout: 2 * time.Second})
		return versionUpdateMsg{version: version}
	}
}
