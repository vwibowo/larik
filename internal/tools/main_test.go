package tools

import (
	"os"
	"testing"
)

// TestMain lets this test binary serve as the run_code script runner,
// which Larik starts as a copy of its own binary.
func TestMain(m *testing.M) {
	ServeCodeIfChild()
	os.Exit(m.Run())
}
