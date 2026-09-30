package mcp

import (
	"fmt"
	"os"
	"testing"
)

// TestMain points the config and data directories at a temporary one, so a
// test that saves settings can't write into the developer's own.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "larik-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("XDG_CONFIG_HOME", dir)
	os.Setenv("XDG_DATA_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
