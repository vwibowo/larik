package permission

import (
	"encoding/json"
	"testing"
)

func TestDesktopPageNavigateAsksPerDomain(t *testing.T) {
	if got := Subject("desktop_page_navigate", json.RawMessage(`{"url":"https://Go.dev/doc"}`)); got != "go.dev" {
		t.Fatalf("subject %q", got)
	}
	if !domainTools["desktop_page_navigate"] || !networkTools["desktop_page_navigate"] {
		t.Fatal("rules name its domain, and plan mode asks for it")
	}
}
