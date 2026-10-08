// Package bench runs small, self-checking coding and context-retention tasks
// against a model so routing presets can be judged by outcome, not just price.
// Every pass/fail check is mechanical rather than judged by another model.
package bench

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Task is one scripted benchmark case.
type Task struct {
	Name string
	// Prompt is given to the agent verbatim.
	Prompt string
	// Setup writes the fixture into dir (a fresh temp directory) and
	// leaves it in a state where Verify would fail.
	Setup func(dir string) error
	// Verify checks whether the task was completed, after the agent has
	// finished. detail explains a failure, or is empty on success.
	Verify func(dir string) (pass bool, detail string)

	// compaction is set only for a context-retention benchmark. Its source
	// conversation is compacted before Prompt asks the model to recover it.
	compaction         *compactionCase
	requireScriptState bool // hybrid/code must use store and load in separate scripts
}

// Tasks are the built-in benchmark cases, in a fixed list.
func Tasks() []Task {
	return []Task{fixOffByOneTask(), implementValidationTask(), renameAcrossFilesTask(), undocumentedTask(), persistentStateTask(), compactionRetentionTask()}
}

// goTest runs `go test ./...` in dir and reports whether it passed.
func goTest(dir string) (bool, string) {
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, "go test ./... failed:\n" + string(out)
	}
	return true, ""
}

func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

func writeModule(dir, module string) error {
	return writeFile(dir, "go.mod", fmt.Sprintf("module %s\n\ngo 1.23\n", module))
}

func fixOffByOneTask() Task {
	const buggy = `package calc

// Sum adds up xs.
func Sum(xs []int) int {
	total := 0
	for i := 1; i < len(xs); i++ {
		total += xs[i]
	}
	return total
}
`
	const test = `package calc

import "testing"

func TestSum(t *testing.T) {
	cases := []struct {
		in   []int
		want int
	}{
		{nil, 0},
		{[]int{5}, 5},
		{[]int{1, 2, 3}, 6},
		{[]int{10, 20, 30, 40}, 100},
	}
	for _, c := range cases {
		if got := Sum(c.in); got != c.want {
			t.Errorf("Sum(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}
`
	return Task{
		Name:   "fix-off-by-one",
		Prompt: "`go test ./...` is failing in this repository. Find the bug and fix it. Don't change the test file.",
		Setup: func(dir string) error {
			if err := writeModule(dir, "fixture"); err != nil {
				return err
			}
			if err := os.Mkdir(filepath.Join(dir, "calc"), 0o755); err != nil {
				return err
			}
			if err := writeFile(dir, "calc/calc.go", buggy); err != nil {
				return err
			}
			return writeFile(dir, "calc/calc_test.go", test)
		},
		Verify: func(dir string) (bool, string) { return goTest(dir) },
	}
}

func implementValidationTask() Task {
	const stub = `package validate

// Email reports whether s looks like a valid email address: it has
// exactly one "@", and both the local part and the domain are
// non-empty and contain no whitespace.
func Email(s string) bool {
	return true // TODO: implement
}
`
	const test = `package validate

import "testing"

func TestEmail(t *testing.T) {
	cases := map[string]bool{
		"a@b.com":       true,
		"first.last@example.co": true,
		"not-an-email":  false,
		"@missing-local.com": false,
		"missing-domain@":    false,
		"two@at@signs.com":   false,
		"has space@b.com":    false,
	}
	for in, want := range cases {
		if got := Email(in); got != want {
			t.Errorf("Email(%q) = %v, want %v", in, got, want)
		}
	}
}
`
	return Task{
		Name:   "implement-validation",
		Prompt: "Implement the Email function in validate/validate.go per its doc comment, so `go test ./...` passes. Don't change the test file.",
		Setup: func(dir string) error {
			if err := writeModule(dir, "fixture"); err != nil {
				return err
			}
			if err := os.Mkdir(filepath.Join(dir, "validate"), 0o755); err != nil {
				return err
			}
			if err := writeFile(dir, "validate/validate.go", stub); err != nil {
				return err
			}
			return writeFile(dir, "validate/validate_test.go", test)
		},
		Verify: func(dir string) (bool, string) { return goTest(dir) },
	}
}

func renameAcrossFilesTask() Task {
	const impl = `package order

// OldName totals the given prices.
func OldName(prices []float64) float64 {
	var total float64
	for _, p := range prices {
		total += p
	}
	return total
}
`
	const caller = `package order

// Receipt formats a total for display.
func Receipt(prices []float64) string {
	total := OldName(prices)
	return formatUSD(total)
}

func formatUSD(v float64) string {
	if v < 0 {
		return "-$" + formatUSD(-v)
	}
	whole := int(v)
	cents := int((v-float64(whole))*100 + 0.5)
	return "$" + itoa(whole) + "." + pad2(cents)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}
`
	const test = `package order

import "testing"

func TestNewName(t *testing.T) {
	if got := NewName([]float64{1, 2, 3.5}); got != 6.5 {
		t.Errorf("NewName = %v, want 6.5", got)
	}
}

func TestReceiptStillWorks(t *testing.T) {
	if got := Receipt([]float64{10, 5}); got != "$15.00" {
		t.Errorf("Receipt = %q, want $15.00", got)
	}
}
`
	return Task{
		Name: "rename-across-files",
		Prompt: "The tests expect a function named NewName, but the code still defines OldName in order/order.go and calls it from order/receipt.go. " +
			"Rename it everywhere it's defined and used so `go test ./...` passes, without changing its behavior.",
		Setup: func(dir string) error {
			if err := writeModule(dir, "fixture"); err != nil {
				return err
			}
			if err := os.Mkdir(filepath.Join(dir, "order"), 0o755); err != nil {
				return err
			}
			if err := writeFile(dir, "order/order.go", impl); err != nil {
				return err
			}
			if err := writeFile(dir, "order/receipt.go", caller); err != nil {
				return err
			}
			return writeFile(dir, "order/order_test.go", test)
		},
		Verify: func(dir string) (bool, string) { return goTest(dir) },
	}
}
