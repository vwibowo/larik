package termbrowser

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestTestExecutableNeverLaunchesRealPane(t *testing.T) {
	s := New(Options{})
	// If the safety check runs too late, this fails before a real launcher
	// could inspect the developer's inherited terminal environment.
	s.paneEnv = func(string) string { t.Fatal("test reached real pane selection"); return "" }
	msg, err := s.showPane(context.Background(), "fake.sock")
	if err == nil || !strings.Contains(err.Error(), "Go test executable") || msg != "" {
		t.Fatalf("showPane = %q, %v", msg, err)
	}
}

func TestInjectedPaneLauncherUsesCapturedArguments(t *testing.T) {
	s := New(Options{})
	s.paneEnv = func(key string) string {
		if key == "CMUX_SURFACE_ID" {
			return "surface:test"
		}
		return ""
	}
	var got []string
	s.paneRun = func(_ context.Context, args []string) error { got = append([]string(nil), args...); return nil }
	msg, err := s.showPane(context.Background(), "/tmp/fake socket")
	if err != nil || !strings.Contains(msg, "new cmux pane") {
		t.Fatalf("showPane = %q, %v", msg, err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cmux", "new-split", "right", "--focus", "false", "--command", shellJoin([]string{exe, "browser", "view", "--socket", "/tmp/fake socket"}), "--surface", "surface:test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("launch args = %q, want %q", got, want)
	}
	s.paneRun = func(context.Context, []string) error { return errors.New("fake launch failure") }
	if _, err := s.showPane(context.Background(), "fake.sock"); err == nil || !strings.Contains(err.Error(), "fake launch failure") {
		t.Fatalf("launch error lost: %v", err)
	}
}
