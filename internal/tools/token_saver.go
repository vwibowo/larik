package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var searchMatch = regexp.MustCompile(`^(.+?):([0-9]+):(.*)$`)

// filterCommandOutput recognizes only simple commands and known output shapes.
// Unknown lines remain verbatim; a caller uses the result only if it is shorter.
// For a command that failed, only test runners are filtered: their filters
// drop passing tests and keep the failures as they are.
func filterCommandOutput(command, raw string, failed bool) (string, bool) {
	command = simpleCommand(command)
	if strings.ContainsAny(command, ";|&<>$()`\n") || strings.TrimSpace(raw) == "" {
		return "", false
	}
	args := strings.Fields(command)
	if len(args) == 0 {
		return "", false
	}
	name := filepath.Base(args[0])
	if failed && !testCommand(name, args) {
		return "", false
	}
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	switch name {
	case "git":
		if len(args) < 2 {
			return "", false
		}
		switch args[1] {
		case "status":
			return filterGitStatus(lines)
		case "log":
			return filterGitLog(lines)
		case "diff":
			if slicesContain(args[2:], "--stat") {
				return filterBlankLines(lines), true
			}
		}
	case "rg", "grep":
		return filterSearch(lines)
	case "find":
		return filterFind(lines)
	case "ls":
		return filterListing(args[1:], lines)
	case "go":
		if len(args) > 1 && args[1] == "test" {
			return filterGoTest(lines)
		}
	case "cargo":
		if len(args) > 1 {
			switch args[1] {
			case "test":
				return filterCargo(lines)
			case "build":
				return filterCargoBuild(lines)
			}
		}
	case "npm", "pnpm", "yarn":
		if len(args) > 1 && (args[1] == "test" || args[1] == "build" || (args[1] == "run" && len(args) > 2 && (args[2] == "test" || args[2] == "build"))) {
			return filterNode(lines)
		}
	case "pytest":
		return filterPytest(lines)
	}
	return "", false
}

// simpleCommand strips what models often wrap around a single command: a
// leading "cd <dir> &&", and a trailing "2>&1" (stderr is captured with
// stdout anyway).
func simpleCommand(command string) string {
	c := strings.TrimSpace(command)
	c = strings.TrimSpace(strings.TrimSuffix(c, "2>&1"))
	if rest, ok := strings.CutPrefix(c, "cd "); ok {
		if dir, cmd, ok := strings.Cut(rest, "&&"); ok && !strings.ContainsAny(strings.TrimSpace(dir), " ;|&<>$()`'\"\\") {
			c = strings.TrimSpace(cmd)
		}
	}
	return c
}

// testCommand reports whether args run a test suite.
func testCommand(name string, args []string) bool {
	switch name {
	case "go", "cargo":
		return len(args) > 1 && args[1] == "test"
	case "npm", "pnpm", "yarn":
		return len(args) > 1 && (args[1] == "test" || args[1] == "run" && len(args) > 2 && args[2] == "test")
	case "pytest":
		return true
	}
	return false
}

func slicesContain(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func filterBlankLines(lines []string) string {
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func filterGitStatus(lines []string) (string, bool) {
	if len(lines) == 0 || (!strings.HasPrefix(lines[0], "On branch ") && !strings.HasPrefix(lines[0], "HEAD detached")) {
		return "", false
	}
	out := []string{lines[0]}
	section, status := "", ""
	var paths []string
	flush := func() {
		if len(paths) == 0 {
			return
		}
		out = append(out, fmt.Sprintf("%s %s (%d):", section, status, len(paths)))
		for _, path := range paths {
			out = append(out, "  "+path)
		}
		paths = nil
	}
	for _, line := range lines[1:] {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "(use ") || strings.HasPrefix(trim, "(nothing ") {
			continue
		}
		if strings.HasPrefix(trim, "no changes added to commit") {
			continue
		}
		switch trim {
		case "Changes to be committed:":
			flush()
			section, status = "staged", ""
		case "Changes not staged for commit:":
			flush()
			section, status = "unstaged", ""
		case "Untracked files:":
			flush()
			section, status = "untracked", ""
		case "Unmerged paths:":
			flush()
			section, status = "unmerged", ""
		default:
			if strings.HasPrefix(line, "\t") {
				if section == "" {
					return "", false
				}
				kind, path := "files", trim
				if section != "untracked" {
					var ok bool
					kind, path, ok = strings.Cut(trim, ":")
					if !ok || strings.TrimSpace(path) == "" {
						return "", false
					}
					path = strings.TrimSpace(path)
				}
				if status != kind {
					flush()
					status = kind
				}
				paths = append(paths, path)
				continue
			}
			flush()
			out = append(out, line)
		}
	}
	flush()
	return strings.Join(out, "\n"), true
}

func filterGitLog(lines []string) (string, bool) {
	if len(lines) < 4 || !strings.HasPrefix(lines[0], "commit ") {
		return "", false
	}
	var out []string
	var hash, author, subject string
	flush := func() {
		if hash != "" {
			out = append(out, strings.TrimSpace(hash+" "+author+" "+subject))
		}
	}
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "commit "):
			flush()
			hash, author, subject = strings.TrimPrefix(line, "commit "), "", ""
		case strings.HasPrefix(line, "Author: "):
			author = strings.TrimPrefix(line, "Author: ")
		case strings.HasPrefix(line, "    ") && subject == "":
			subject = strings.TrimSpace(line)
		case strings.TrimSpace(line) == "", strings.HasPrefix(line, "Date: "):
		default:
			return "", false // unfamiliar metadata must remain exact
		}
	}
	flush()
	return strings.Join(out, "\n"), len(out) > 0
}

func filterSearch(lines []string) (string, bool) {
	var out []string
	last := ""
	for _, line := range lines {
		m := searchMatch.FindStringSubmatch(line)
		if m == nil {
			return "", false
		}
		if m[1] != last {
			out = append(out, m[1]+":")
			last = m[1]
		}
		out = append(out, "  "+m[2]+":"+m[3])
	}
	return strings.Join(out, "\n"), len(out) > 1
}

func filterFind(lines []string) (string, bool) {
	var out []string
	lastDir := ""
	for _, line := range lines {
		if line == "" || !(strings.HasPrefix(line, "./") || strings.HasPrefix(line, "/")) {
			return "", false
		}
		dir, base := filepath.Dir(line), filepath.Base(line)
		if dir != lastDir {
			out = append(out, dir+"/")
			lastDir = dir
		}
		out = append(out, "  "+base)
	}
	return strings.Join(out, "\n"), len(out) > 1
}

func filterListing(args, lines []string) (string, bool) {
	if !slicesContain(args, "-l") {
		return "", false
	}
	var out []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), len(out) > 0
}

func filterGoTest(lines []string) (string, bool) {
	passed := 0
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(line, "ok  ") || strings.HasPrefix(line, "ok\t") {
			passed++
			continue
		}
		if strings.HasPrefix(line, "?   ") || strings.HasPrefix(line, "?\t") {
			out = append(out, line)
			continue
		}
		if line == "PASS" || strings.TrimSpace(line) == "" || goTestNoise(line) {
			continue
		}
		out = append(out, line)
	}
	if passed == 0 {
		return "", false
	}
	return strings.Join(append([]string{fmt.Sprintf("PASS %d Go package(s)", passed)}, out...), "\n"), true
}

// goTestNoise reports the lines go test -v prints for tests that pass:
// starts, pauses and passes. Failures and skips stay.
func goTestNoise(line string) bool {
	trim := strings.TrimLeft(line, " ")
	for _, p := range []string{"=== RUN ", "=== PAUSE ", "=== CONT ", "=== NAME ", "--- PASS: "} {
		if strings.HasPrefix(trim, p) {
			return true
		}
	}
	return false
}

func filterCargo(lines []string) (string, bool) {
	passed := 0
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(line, "test ") && strings.HasSuffix(line, " ... ok") {
			passed++
			continue
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	if passed == 0 {
		return "", false
	}
	return strings.Join(append([]string{fmt.Sprintf("PASS %d Cargo test(s)", passed)}, out...), "\n"), true
}

func filterCargoBuild(lines []string) (string, bool) {
	compiled := 0
	var out []string
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "Compiling ") {
			compiled++
			continue
		}
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	if compiled == 0 {
		return "", false
	}
	return strings.Join(append([]string{fmt.Sprintf("Compiled %d crate(s)", compiled)}, out...), "\n"), true
}

func filterNode(lines []string) (string, bool) {
	passed := 0
	var out []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "✓ ") || strings.HasPrefix(trim, "✔ ") {
			passed++
			continue
		}
		if strings.HasPrefix(trim, "npm notice") || trim == "" {
			continue
		}
		out = append(out, line)
	}
	if passed == 0 && len(out) == len(lines) {
		return "", false
	}
	if passed > 0 {
		out = append([]string{fmt.Sprintf("PASS %d test(s)", passed)}, out...)
	}
	return strings.Join(out, "\n"), true
}

func filterPytest(lines []string) (string, bool) {
	if len(lines) == 0 || !strings.Contains(lines[len(lines)-1], " passed") {
		return "", false
	}
	var out []string
	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if strings.Contains(trim, " passed") {
			out = append(out, strings.Trim(trim, "= "))
			continue
		}
		if trim == "" || strings.HasPrefix(trim, "=") || (strings.HasSuffix(trim, " [100%]") && strings.HasPrefix(trim, "tests/")) {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n"), true
}
