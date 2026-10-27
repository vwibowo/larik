package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Weaker models copy the working directory wrong, such as
// /var/folders/tc/x/T/... written as /var/folders/tc/x_T/..., and then
// spend their turns listing directories to find out why nothing exists:
// two NIM bench runs ran out of turns this way. When a call fails on a
// path that is the working directory with a character or two changed,
// pathNote points at the path that exists.

var absPath = regexp.MustCompile(`(?:^|[\s'"=:(])(/[^\s'"` + "`" + `;|&<>()]+)`)

// pathNote is a note for a result that says a path doesn't exist, when the
// call's input names a path one or two edits from one under cwd.
func pathNote(cwd string, input json.RawMessage, content string) string {
	if cwd == "" || !strings.Contains(strings.ToLower(content), "no such file or directory") {
		return ""
	}
	var v any
	if json.Unmarshal(input, &v) != nil {
		return ""
	}
	for _, s := range stringsIn(v) {
		for _, m := range absPath.FindAllStringSubmatch(s, -1) {
			p := m[1]
			if _, err := os.Stat(p); err == nil {
				continue
			}
			if fixed := nearCwd(p, cwd); fixed != "" {
				return fmt.Sprintf("[note: %s does not exist. The working directory is %s: did you mean %s? Paths relative to the working directory work too.]", p, cwd, fixed)
			}
		}
	}
	return ""
}

// nearCwd returns p with its first part replaced by cwd, when that part
// is at most two edits from cwd (or cwd with symlinks resolved, such as
// /private/var for /var on macOS) and the result exists.
func nearCwd(p, cwd string) string {
	forms := []string{cwd}
	if real, err := filepath.EvalSymlinks(cwd); err == nil && real != cwd {
		forms = append(forms, real)
	}
	for _, c := range forms {
		for k := len(c) - 2; k <= len(c)+2; k++ {
			if k < 1 || k > len(p) || (k < len(p) && p[k] != '/') {
				continue
			}
			if head := p[:k]; head == c || editDistance(head, c) > 2 {
				continue
			}
			fixed := c + p[k:]
			if _, err := os.Stat(fixed); err == nil {
				return fixed
			}
		}
	}
	return ""
}

// stringsIn lists the strings in a decoded JSON value.
func stringsIn(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, e := range v {
			out = append(out, stringsIn(e)...)
		}
		return out
	case map[string]any:
		var out []string
		for _, e := range v {
			out = append(out, stringsIn(e)...)
		}
		return out
	}
	return nil
}

// editDistance is the Levenshtein distance between a and b, in bytes.
func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}
