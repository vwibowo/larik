package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

const latestReleaseURL = "https://api.github.com/repos/vwibowo/larik/releases/latest"

type versionUpdateMsg struct{ version string }

type releaseInfo struct {
	TagName string `json:"tag_name"`
}

// checkVersion checks the public latest release without delaying TUI startup.
// A failed check is deliberately silent: version discovery is only a
// convenience and must not make Larik unavailable offline.
func checkVersion(current string) tea.Cmd {
	return func() tea.Msg {
		version, _ := fetchLatestVersion(current, latestReleaseURL, &http.Client{Timeout: 2 * time.Second})
		return versionUpdateMsg{version: version}
	}
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func fetchLatestVersion(current, url string, client httpDoer) (string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Larik/"+current)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("release check returned HTTP %d", resp.StatusCode)
	}
	var release releaseInfo
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	version, _ := newerVersion(current, release.TagName)
	return version, nil
}

func newerVersion(current, latest string) (string, bool) {
	cur, ok := parseVersion(current)
	if !ok {
		return "", false
	}
	next, ok := parseVersion(latest)
	if !ok || compareVersion(next, cur) <= 0 {
		return "", false
	}
	return "v" + formatVersion(next), true
}

type releaseVersion [3]int

func parseVersion(s string) (releaseVersion, bool) {
	var v releaseVersion
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != len(v) {
		return v, false
	}
	for i, part := range parts {
		if part == "" {
			return v, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

func compareVersion(a, b releaseVersion) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

func formatVersion(v releaseVersion) string {
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
}
