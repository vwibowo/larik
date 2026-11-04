// Package release finds out whether a newer larik has been published.
package release

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// LatestURL is the public latest release on GitHub.
const LatestURL = "https://api.github.com/repos/vwibowo/larik/releases/latest"

// NotesURL is the page of the latest release, for people to read.
const NotesURL = "https://github.com/vwibowo/larik/releases/latest"

// UpdateCommand installs the latest release over the current one.
const UpdateCommand = "curl -fsSL https://raw.githubusercontent.com/vwibowo/larik/main/scripts/get-larik.sh | sh"

// Doer sends a request, as *http.Client does.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Newer returns the latest release's version ("v0.29.0") when it is newer
// than current, and "" when it isn't or current isn't a release version,
// as a development build's.
func Newer(ctx context.Context, current, url string, client Doer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	version, _ := NewerVersion(current, release.TagName)
	return version, nil
}

// NewerVersion returns latest as "vX.Y.Z" when it is a newer release
// version than current.
func NewerVersion(current, latest string) (string, bool) {
	cur, ok := parse(current)
	if !ok {
		return "", false
	}
	next, ok := parse(latest)
	if !ok || compare(next, cur) <= 0 {
		return "", false
	}
	return fmt.Sprintf("v%d.%d.%d", next[0], next[1], next[2]), true
}

type version [3]int

func parse(s string) (version, bool) {
	var v version
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

func compare(a, b version) int {
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
