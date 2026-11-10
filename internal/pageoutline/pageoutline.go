// Package pageoutline shares the page snapshot format and renderer used by
// Larik Desktop's browser tools.
package pageoutline

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"larik/internal/tools"
)

//go:embed snapshot.js
var snapshotJS string

// Snapshot is a browser page's text outline and interactive element count.
type Snapshot struct {
	URL   string `json:"url"`
	Title string `json:"title"`
	Text  string `json:"text"`
	Refs  int    `json:"refs"`
	Error string `json:"error"`
}

// SnapshotScript returns the page script that outlines a page or one element.
func SnapshotScript(ref string) string { return snapshotJS + "(" + strconv.Quote(ref) + ")" }

// RenderSnapshot formats a snapshot from character offset start, preceded by
// a short description of the action that produced it.
func RenderSnapshot(snap Snapshot, did string, start int) tools.Result {
	const maxSnapshot = 20_000
	var b strings.Builder
	if did != "" {
		b.WriteString(did + "\n")
	}
	fmt.Fprintf(&b, "URL: %s\nTitle: %s\n<web_content url=%q>\n", snap.URL, snap.Title, snap.URL)
	text := snap.Text
	total := len(text)
	start = min(max(start, 0), total)
	if start > 0 && start < total && text[start-1] != '\n' {
		if nl := strings.IndexByte(text[start:], '\n'); nl >= 0 {
			start += nl + 1
		}
	}
	end := total
	if end-start > maxSnapshot {
		end = start + maxSnapshot
		if cut := strings.LastIndexByte(text[start:end], '\n'); cut > 0 {
			end = start + cut + 1
		}
	}
	b.WriteString(strings.TrimRight(text[start:end], "\n") + "\n</web_content>")
	if start > 0 || end < total {
		fmt.Fprintf(&b, "\n[showing characters %d-%d of %d", start, end, total)
		if end < total {
			fmt.Fprintf(&b, "; call desktop_page_snapshot with start=%d for more, or with ref to read one part of the page", end)
		}
		b.WriteString("]")
	}
	host := snap.URL
	if u, err := url.Parse(snap.URL); err == nil && u.Host != "" {
		host = u.Host
	}
	return tools.Result{Content: b.String(), Display: fmt.Sprintf("%s · %d refs", firstNonEmpty(snap.Title, host), snap.Refs)}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// Decode decodes the JSON value returned by snapshot.js.
func Decode(data []byte) (*Snapshot, error) {
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}
