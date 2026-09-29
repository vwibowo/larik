package trace

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Prune deletes traces under sessionsRoot (every project's session
// directory) last written more than keep ago.
func Prune(sessionsRoot string, keep time.Duration) error {
	projects, err := os.ReadDir(sessionsRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	cutoff := time.Now().Add(-keep)
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		dir := filepath.Join(sessionsRoot, p.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || !strings.HasSuffix(e.Name(), ".trace") {
				continue
			}
			events := filepath.Join(dir, e.Name(), EventsFile)
			fi, err := os.Stat(events)
			if err != nil {
				fi, err = e.Info()
			}
			if err == nil && fi.ModTime().Before(cutoff) {
				os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}
	return nil
}
