package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
)

// browserBookmark is independent of an open pane. Remote pages keep the
// destination instead of the temporary local port used by their SSH forward.
type browserBookmark struct {
	Title      string `json:"title"`
	URL        string `json:"url,omitempty"`
	RemoteName string `json:"remote_name,omitempty"`
	RemotePort int    `json:"remote_port,omitempty"`
	RemotePath string `json:"remote_path,omitempty"`
}

func (b browserBookmark) key() string {
	if b.RemotePort != 0 {
		return "remote:" + b.RemoteName + ":" + strconv.Itoa(b.RemotePort) + ":" + b.RemotePath
	}
	return "url:" + b.URL
}

func (b browserBookmark) label() string {
	if b.Title != "" {
		return b.Title
	}
	if b.RemotePort != 0 {
		return fmt.Sprintf("%s:%d", b.RemoteName, b.RemotePort)
	}
	return pageHost(b.URL)
}

func (b browserBookmark) address() string {
	if b.RemotePort != 0 {
		return fmt.Sprintf("%s:%d%s", b.RemoteName, b.RemotePort, b.RemotePath)
	}
	return b.URL
}

func bookmarkSuffix(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	s := u.EscapedPath()
	if u.RawQuery != "" {
		s += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		s += "#" + u.EscapedFragment()
	}
	return s
}

func bookmarkForTab(t *tab) (browserBookmark, bool) {
	if t == nil || !t.browser || t.url == "" || t.url == blankPage {
		return browserBookmark{}, false
	}
	b := browserBookmark{Title: t.title, URL: t.url}
	if t.remotePort.port != 0 {
		b.URL = ""
		b.RemoteName, b.RemotePort = t.remotePort.name, t.remotePort.port
		b.RemotePath = bookmarkSuffix(t.url)
	}
	return b, true
}

func tabMatchesBookmark(t *tab, b browserBookmark) bool {
	if b.RemotePort != 0 && t != nil && t.browser && t.remotePort.name == b.RemoteName && t.remotePort.port == b.RemotePort && t.remoteBookmarkSuffix == b.RemotePath {
		return true
	}
	current, ok := bookmarkForTab(t)
	return ok && current.key() == b.key()
}

type bookmarkStore struct {
	mu    sync.RWMutex
	path  string
	items []browserBookmark
	err   error
}

func loadBookmarkStore(dataDir string) *bookmarkStore {
	s := &bookmarkStore{}
	if dataDir == "" {
		return s
	}
	s.path = filepath.Join(dataDir, "desktop", "bookmarks.json")
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s
	}
	if err != nil {
		s.err = err
		return s
	}
	var saved struct {
		Bookmarks []browserBookmark `json:"bookmarks"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		s.err = err
		return s
	}
	s.items = saved.Bookmarks
	return s
}

func (s *bookmarkStore) all() []browserBookmark {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clone(s.items)
}

func (s *bookmarkStore) has(b browserBookmark) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, item := range s.items {
		if item.key() == b.key() {
			return true
		}
	}
	return false
}

func (s *bookmarkStore) set(b browserBookmark, add bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	next := slices.Clone(s.items)
	index := slices.IndexFunc(next, func(x browserBookmark) bool { return x.key() == b.key() })
	if add && index < 0 {
		next = append(next, b)
	} else if !add && index >= 0 {
		next = slices.Delete(next, index, index+1)
	} else {
		return nil
	}
	if s.path != "" {
		data, err := json.Marshal(struct {
			Bookmarks []browserBookmark `json:"bookmarks"`
		}{next})
		if err != nil {
			return err
		}
		if err := writeState(s.path, data); err != nil {
			return err
		}
	}
	s.items = next
	return nil
}

func (a *App) setBookmark(b browserBookmark, add bool) {
	if err := a.bookmarks.set(b, add); err != nil {
		a.notice = "Can't save browser bookmarks: " + err.Error()
		return
	}
	if a.bookmarksChanged != nil {
		a.bookmarksChanged()
	}
}

func (a *App) toggleBookmark(t *tab) {
	if b, ok := bookmarkForTab(t); ok {
		a.setBookmark(b, !a.bookmarks.has(b))
	}
}

func (a *App) openSavedBookmark(b browserBookmark) {
	if a.openBookmark != nil {
		a.openBookmark(b)
		return
	}
	for _, t := range a.tabs {
		if tabMatchesBookmark(t, b) {
			a.activate(t)
			if t.remotePortLost {
				a.startRemoteForward(t.remotePort, false, t, "")
			}
			return
		}
	}
	if b.RemotePort != 0 {
		a.openRemoteBookmark(b)
		return
	}
	a.openBrowser(b.URL)
}
