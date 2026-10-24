package termbrowser

import (
	"context"
	"strings"
	"testing"
	"time"
)

// OpenURL gives its owner a tab of its own: the main agent's page stays.
func TestOpenURLUsesItsOwnTab(t *testing.T) {
	s, srv := newSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := s.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if err := s.OpenURL(ctx, "trace", srv.URL+"/other"); err != nil {
		t.Fatal(err)
	}
	title, err := s.Eval(ctx, "document.title")
	if err != nil || !strings.Contains(title, "Test form") {
		t.Fatalf("the main agent's tab changed: %q %v", title, err)
	}
	s.mu.Lock()
	owned := 0
	for _, tb := range s.tabs {
		if tb.owner == "trace" {
			owned++
		}
	}
	s.mu.Unlock()
	if owned != 1 {
		t.Fatalf("tabs owned by trace = %d, want 1", owned)
	}
}

// The address checks of browser_navigate apply.
func TestOpenURLRefusesBlockedAddresses(t *testing.T) {
	s := New(Options{})
	defer s.Close()
	if err := s.OpenURL(context.Background(), "trace", "http://169.254.169.254/latest"); err == nil {
		t.Fatal("a metadata address must be refused")
	}
	if err := s.OpenURL(context.Background(), "trace", "file:///etc/passwd"); err == nil {
		t.Fatal("only http and https can be opened")
	}
}
