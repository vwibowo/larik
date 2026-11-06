package desktop

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestStateWriteFailureRetriesUnchangedState(t *testing.T) {
	a := New(Options{DataDir: t.TempDir()})
	a.restored = true
	path := a.statePath()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	a.saveState()
	a.writing.Wait()
	if a.saveError == "" || a.persisted != "" || a.savePending != 0 {
		t.Fatalf("failed write was treated as saved: error=%q persisted=%q pending=%d", a.saveError, a.persisted, a.savePending)
	}
	queued := a.saves
	a.saveState()
	if a.saves != queued {
		t.Fatal("state was retried before the five-second delay")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	a.saveRetryAt = time.Now().Add(-time.Second)
	a.saveState()
	a.writing.Wait()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != a.saved || a.persisted != a.saved || a.saveError != "" {
		t.Fatalf("unchanged state did not recover: read=%v saved=%q persisted=%q error=%q", err, a.saved, a.persisted, a.saveError)
	}
	a.Close()
}

func TestCloseFlushesStateAfterFailedBackgroundWrite(t *testing.T) {
	a := New(Options{DataDir: t.TempDir()})
	a.restored = true
	path := a.statePath()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	a.saveState()
	a.writing.Wait()
	if a.saveError == "" {
		t.Fatal("the initial write unexpectedly succeeded")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	a.Close() // the retry deadline has not passed, but close must flush.
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"version":4`) {
		t.Fatalf("close did not write state: %v %q", err, data)
	}
}
