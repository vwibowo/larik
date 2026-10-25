package bench

import (
	"os"
	"path/filepath"
	"strings"
)

// The tasks here have a shortcut: a change that makes the visible test
// pass without doing what was asked. Verify runs checks the agent never
// saw, so a model (or a harness) that settles for the shortcut fails.
// They tell setups apart where the easy tasks all pass.

// hiddenTest runs go test with an extra test file the agent never saw,
// after the visible tests pass, and removes it again.
func hiddenTest(dir, rel, content string) (bool, string) {
	if pass, detail := goTest(dir); !pass {
		return false, detail
	}
	path := filepath.Join(dir, rel)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return false, "writing the hidden test: " + err.Error()
	}
	defer os.Remove(path)
	if pass, detail := goTest(dir); !pass {
		return false, "the visible tests pass, but the hidden ones don't:\n" + detail
	}
	return true, ""
}

// unchanged reports, as a failure detail, a file the agent was told not
// to change but did.
func unchanged(dir, rel, want string) string {
	data, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil || string(data) != want {
		return rel + " was changed, though the task said not to"
	}
	return ""
}

// writeFiles writes a fixture: a Go module, then files by relative path,
// creating their directories.
func writeFiles(dir string, files map[string]string) error {
	if err := writeModule(dir, "fixture"); err != nil {
		return err
	}
	for rel, content := range files {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
			return err
		}
		if err := writeFile(dir, rel, content); err != nil {
			return err
		}
	}
	return nil
}

// overfitTask has a one-case test that adding 1 satisfies; the doc
// comment, which hidden cases check, needs rounding up.
func overfitTask() Task {
	const buggy = `package pager

// Pages returns how many pages it takes to show total items, perPage to
// a page: a partly filled last page counts, and no items need no pages.
// perPage is always positive.
func Pages(total, perPage int) int {
	return total / perPage
}
`
	const test = `package pager

import "testing"

func TestPages(t *testing.T) {
	if got := Pages(10, 3); got != 4 {
		t.Errorf("Pages(10, 3) = %d, want 4", got)
	}
}
`
	const hidden = `package pager

import "testing"

func TestPagesHidden(t *testing.T) {
	for _, c := range []struct{ total, per, want int }{
		{10, 3, 4}, {9, 3, 3}, {0, 5, 0}, {1, 1, 1}, {5, 10, 1}, {20, 5, 4}, {21, 5, 5},
	} {
		if got := Pages(c.total, c.per); got != c.want {
			t.Errorf("Pages(%d, %d) = %d, want %d", c.total, c.per, got, c.want)
		}
	}
}
`
	return Task{
		Name:   "fix-without-overfitting",
		Prompt: "`go test ./...` is failing in this repository. Find the bug and fix it. Don't change the test file.",
		Setup: func(dir string) error {
			return writeFiles(dir, map[string]string{"pager/pager.go": buggy, "pager/pager_test.go": test})
		},
		Verify: func(dir string) (bool, string) {
			if d := unchanged(dir, "pager/pager_test.go", test); d != "" {
				return false, d
			}
			return hiddenTest(dir, "pager/hidden_test.go", hidden)
		},
	}
}

// sourceTask's bug is in a shared parser, but the failing test is in one
// of its callers, where a local workaround is the shortest fix. A hidden
// test checks the parser and its other caller.
func sourceTask() Task {
	const units = `package units

import (
	"strconv"
	"strings"
	"time"
)

// ParseTimeout parses a timeout setting: a Go duration such as "30s" or
// "500ms", or a bare number of seconds such as "5".
func ParseTimeout(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Millisecond, nil
	}
	return time.ParseDuration(s)
}
`
	const client = `package client

import (
	"time"

	"fixture/units"
)

// Client talks to the backend.
type Client struct{ Timeout time.Duration }

// New builds a client from its settings. "timeout" defaults to 10 seconds.
func New(settings map[string]string) (*Client, error) {
	c := &Client{Timeout: 10 * time.Second}
	if v, ok := settings["timeout"]; ok {
		d, err := units.ParseTimeout(v)
		if err != nil {
			return nil, err
		}
		c.Timeout = d
	}
	return c, nil
}
`
	const server = `package server

import (
	"time"

	"fixture/units"
)

// IdleTimeout is how long the server keeps an idle connection open,
// from the "idle_timeout" setting, 60 seconds by default.
func IdleTimeout(settings map[string]string) (time.Duration, error) {
	v, ok := settings["idle_timeout"]
	if !ok {
		return 60 * time.Second, nil
	}
	return units.ParseTimeout(v)
}
`
	const test = `package client

import (
	"testing"
	"time"
)

func TestTimeoutSetting(t *testing.T) {
	for in, want := range map[string]time.Duration{"5": 5 * time.Second, "250ms": 250 * time.Millisecond} {
		c, err := New(map[string]string{"timeout": in})
		if err != nil {
			t.Fatal(err)
		}
		if c.Timeout != want {
			t.Errorf("timeout %q = %v, want %v", in, c.Timeout, want)
		}
	}
}
`
	const hidden = `package server

import (
	"testing"
	"time"

	"fixture/units"
)

func TestTimeoutsHidden(t *testing.T) {
	for in, want := range map[string]time.Duration{"5": 5 * time.Second, " 30 ": 30 * time.Second, "1m": time.Minute, "500ms": 500 * time.Millisecond} {
		if got, err := units.ParseTimeout(in); err != nil || got != want {
			t.Errorf("ParseTimeout(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if got, err := IdleTimeout(map[string]string{"idle_timeout": "90"}); err != nil || got != 90*time.Second {
		t.Errorf("IdleTimeout(90) = %v, %v; want 1m30s", got, err)
	}
}
`
	return Task{
		Name: "fix-at-the-source",
		Prompt: "Setting the client's timeout to \"5\" gives a 5 millisecond timeout instead of 5 seconds, and `go test ./...` fails. " +
			"Fix the bug. Don't change the test file.",
		Setup: func(dir string) error {
			return writeFiles(dir, map[string]string{
				"units/units.go": units, "client/client.go": client, "client/client_test.go": test, "server/server.go": server,
			})
		},
		Verify: func(dir string) (bool, string) {
			if d := unchanged(dir, "client/client_test.go", test); d != "" {
				return false, d
			}
			return hiddenTest(dir, "server/hidden_test.go", hidden)
		},
	}
}

// changelogPreamble is the fixture changelog up to the Unreleased section,
// and changelogReleased the history after it, which must stay as it is.
const (
	changelogPreamble = "# Changelog\n\n## Unreleased\n"
	changelogReleased = "## 1.2.0\n\n- Added: Slugify.\n"
)

// conventionTask's fix is easy; what it checks is whether the agent
// follows the project's AGENTS.md, which asks for a changelog entry with
// every behavior change. The prompt doesn't mention it.
func conventionTask() Task {
	const agents = `# Working in this repository

- Every change in behavior gets a line under "## Unreleased" in CHANGELOG.md, such as "- Fixed: <what>" or "- Added: <what>". Never edit released sections.
- Run ` + "`go test ./...`" + ` before you finish.
`
	const buggy = `package text

import "strings"

// Slugify turns a title into a URL slug: words joined by hyphens.
func Slugify(title string) string {
	return strings.Join(strings.Fields(title), "-")
}
`
	const test = `package text

import "testing"

func TestSlugify(t *testing.T) {
	if got := Slugify("  many   spaces here "); got != "many-spaces-here" {
		t.Errorf("Slugify = %q", got)
	}
}
`
	const hidden = `package text

import "testing"

func TestSlugifyHidden(t *testing.T) {
	for in, want := range map[string]string{"Hello World": "hello-world", "  many   spaces here ": "many-spaces-here", "GO": "go"} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
`
	return Task{
		Name:   "follow-conventions",
		Prompt: "Slugify should return lowercase slugs: Slugify(\"Hello World\") should be \"hello-world\", but today it keeps the capitals. Fix it.",
		Setup: func(dir string) error {
			return writeFiles(dir, map[string]string{
				"AGENTS.md": agents, "CHANGELOG.md": changelogPreamble + "\n" + changelogReleased,
				"text/slug.go": buggy, "text/slug_test.go": test,
			})
		},
		Verify: func(dir string) (bool, string) {
			if pass, detail := hiddenTest(dir, "text/hidden_test.go", hidden); !pass {
				return false, detail
			}
			return checkChangelog(dir)
		},
	}
}

// checkChangelog wants a new entry under Unreleased and the released
// history untouched.
func checkChangelog(dir string) (bool, string) {
	data, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	if err != nil {
		return false, "CHANGELOG.md is missing"
	}
	log := string(data)
	start := strings.Index(log, "## Unreleased")
	end := strings.Index(log, "## 1.2.0")
	if start < 0 || end < start {
		return false, "CHANGELOG.md lost its Unreleased or 1.2.0 heading"
	}
	if !strings.HasSuffix(log, changelogReleased) {
		return false, "the released 1.2.0 section of CHANGELOG.md was edited"
	}
	for _, line := range strings.Split(log[start:end], "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			return true, ""
		}
	}
	return false, "the code is fixed, but CHANGELOG.md has no entry under Unreleased, which AGENTS.md asks for"
}
