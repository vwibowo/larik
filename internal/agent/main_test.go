package agent

import (
	"os"
	"testing"

	"larik/internal/tools"
)

// TestMain lets this test binary serve as the run_code script runner,
// which Larik starts as a copy of its own binary.
func TestMain(m *testing.M) {
	tools.ServeCodeIfChild()
	os.Exit(m.Run())
}
