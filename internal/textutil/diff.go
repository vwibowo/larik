package textutil

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// diffContext is how many unchanged lines Diff keeps around a change.
	diffContext = 3
	// maxDiffCells caps the comparison table. Inputs past it are compared
	// only by their shared start and end, which still gives a correct diff.
	maxDiffCells = 1 << 22
)

// Span is a byte range of a string.
type Span struct{ Start, End int }

// Diff compares old and new line by line for display: removed lines start
// with "- ", added lines with "+ " and unchanged ones with two spaces. Long
// unchanged runs between changes are replaced by a "  ⋯" line, and those
// before the first change or after the last are dropped.
func Diff(old, new string) string {
	if strings.HasSuffix(old, "\n") && strings.HasSuffix(new, "\n") {
		old, new = old[:len(old)-1], new[:len(new)-1]
	}
	a, b := diffLines(old), diffLines(new)
	keptA, keptB := common(a, b)
	var rows []string
	for i, j := 0, 0; i < len(a) || j < len(b); {
		switch {
		case i < len(a) && !keptA[i]:
			rows = append(rows, "- "+a[i])
			i++
		case j < len(b) && !keptB[j]:
			rows = append(rows, "+ "+b[j])
			j++
		default:
			rows = append(rows, "  "+a[i])
			i, j = i+1, j+1
		}
	}
	return strings.Join(trimContext(rows), "\n")
}

func diffLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// trimContext keeps the changed rows and the unchanged ones near them.
func trimContext(rows []string) []string {
	near := make([]bool, len(rows))
	for i, r := range rows {
		if strings.HasPrefix(r, "  ") {
			continue
		}
		for j := max(i-diffContext, 0); j <= min(i+diffContext, len(rows)-1); j++ {
			near[j] = true
		}
	}
	var out []string
	skipped := 0
	for i, r := range rows {
		if !near[i] {
			skipped++
			continue
		}
		if skipped == 1 && len(out) > 0 {
			out = append(out, rows[i-1]) // a marker would hide nothing
		} else if skipped > 1 && len(out) > 0 {
			out = append(out, "  ⋯")
		}
		skipped = 0
		out = append(out, r)
	}
	return out
}

// WordDiff finds the changed parts of a pair of lines: the spans of old that
// were removed and of new that were added. Lines with too little in common
// to be edits of each other return nil, since marking nearly everything
// tells the reader nothing.
func WordDiff(old, new string) (oldSpans, newSpans []Span) {
	a, b := tokens(old), tokens(new)
	keptA, keptB := common(a, b)
	same := 0
	for i, t := range a {
		if keptA[i] && strings.TrimSpace(t) != "" {
			same += len(t)
		}
	}
	if same*3 < max(len(strings.TrimSpace(old)), len(strings.TrimSpace(new))) {
		return nil, nil
	}
	return changedSpans(old, a, keptA), changedSpans(new, b, keptB)
}

// tokens splits s into words, runs of whitespace and single other characters.
func tokens(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		end := i + n
		if class := tokenClass(r); class != 0 {
			for end < len(s) {
				next, n := utf8.DecodeRuneInString(s[end:])
				if tokenClass(next) != class {
					break
				}
				end += n
			}
		}
		out = append(out, s[i:end])
		i = end
	}
	return out
}

func tokenClass(r rune) int {
	switch {
	case unicode.IsSpace(r):
		return 1
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		return 2
	}
	return 0
}

// changedSpans returns the tokens of s not kept, as byte ranges, joining
// those separated only by whitespace.
func changedSpans(s string, toks []string, kept []bool) []Span {
	var out []Span
	at := 0
	for i, t := range toks {
		if !kept[i] {
			if n := len(out); n > 0 && strings.TrimSpace(s[out[n-1].End:at]) == "" {
				out[n-1].End = at + len(t)
			} else {
				out = append(out, Span{at, at + len(t)})
			}
		}
		at += len(t)
	}
	return out
}

// common marks the elements of a and b that belong to a longest common
// subsequence of the two.
func common(a, b []string) (keptA, keptB []bool) {
	keptA, keptB = make([]bool, len(a)), make([]bool, len(b))
	start := 0
	for start < len(a) && start < len(b) && a[start] == b[start] {
		keptA[start], keptB[start] = true, true
		start++
	}
	endA, endB := len(a), len(b)
	for endA > start && endB > start && a[endA-1] == b[endB-1] {
		endA, endB = endA-1, endB-1
		keptA[endA], keptB[endB] = true, true
	}
	n, m := endA-start, endB-start
	if n == 0 || m == 0 || n > maxDiffCells/m {
		return keptA, keptB
	}
	// table[i*(m+1)+j] is the common length of a[start+i:endA] and b[start+j:endB].
	table := make([]int32, (n+1)*(m+1))
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[start+i] == b[start+j] {
				table[i*(m+1)+j] = table[(i+1)*(m+1)+j+1] + 1
			} else {
				table[i*(m+1)+j] = max(table[(i+1)*(m+1)+j], table[i*(m+1)+j+1])
			}
		}
	}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a[start+i] == b[start+j]:
			keptA[start+i], keptB[start+j] = true, true
			i, j = i+1, j+1
		case table[(i+1)*(m+1)+j] >= table[i*(m+1)+j+1]:
			i++
		default:
			j++
		}
	}
	return keptA, keptB
}
