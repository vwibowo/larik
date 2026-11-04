package tui

import (
	"strings"
	"testing"
)

func TestVersionUpdateMessageIsShown(t *testing.T) {
	m := testModel(t)
	m.opts.Version = "0.7.0"
	_, cmd := m.Update(versionUpdateMsg{version: "v0.8.0"})
	m.Update(cmd())
	if !strings.Contains(plain(m.outputs[0].text), "update available: v0.8.0 (current v0.7.0)") {
		t.Fatalf("update notice missing from startup output: %q", m.outputs[0].text)
	}
}
