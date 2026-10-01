package tools

import "testing"

// SetCodeMemoryLimit lowers run_code's memory limit for one test, for
// tests outside the package.
func SetCodeMemoryLimit(t *testing.T, mb uint64) {
	t.Helper()
	old := codeMemoryLimit
	codeMemoryLimit = mb << 20
	t.Cleanup(func() { codeMemoryLimit = old })
}
