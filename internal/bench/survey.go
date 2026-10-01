package bench

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Survey shape: enough files that reading them one tool call at a time
// fills the context, which is where run_code scripts should pay off.
const (
	surveyPackages = 8
	surveyFiles    = 5
	surveyFuncs    = 6
)

// undocumentedTask asks for a report that needs every file read but
// little judgment: a survey, not an edit.
func undocumentedTask() Task {
	return Task{
		Name: "find-undocumented",
		Prompt: "List every exported top-level function in this repository that has no doc comment directly above it. " +
			"Write them to UNDOCUMENTED.txt in the repository root, one per line as package.Function (for example pkg01.Compute3), sorted, and nothing else. Don't change any other file.",
		Setup: func(dir string) error {
			_, err := writeSurvey(dir)
			return err
		},
		Verify: func(dir string) (bool, string) {
			want := surveyAnswer()
			data, err := os.ReadFile(filepath.Join(dir, "UNDOCUMENTED.txt"))
			if err != nil {
				return false, "UNDOCUMENTED.txt is missing"
			}
			var got []string
			for _, l := range strings.Split(string(data), "\n") {
				if l = strings.TrimSpace(l); l != "" {
					got = append(got, l)
				}
			}
			if !slices.Equal(got, want) {
				return false, fmt.Sprintf("got %d names, want %d:\ngot:  %s\nwant: %s", len(got), len(want), strings.Join(got, " "), strings.Join(want, " "))
			}
			return true, ""
		},
	}
}

// surveyUndocumented decides which exported functions lack a doc comment,
// spread unevenly over packages and files.
func surveyUndocumented(p, f, i int) bool { return (p*7+f*3+i*5)%9 == 0 }

func surveyAnswer() []string {
	var out []string
	for p := 1; p <= surveyPackages; p++ {
		for f := 1; f <= surveyFiles; f++ {
			for i := 1; i <= surveyFuncs; i++ {
				if surveyUndocumented(p, f, i) {
					out = append(out, fmt.Sprintf("pkg%02d.%s", p, surveyName(f, i)))
				}
			}
		}
	}
	slices.Sort(out)
	return out
}

func surveyName(f, i int) string {
	verbs := []string{"Compute", "Parse", "Render", "Merge", "Check"}
	return fmt.Sprintf("%s%d", verbs[f-1], i)
}

// writeSurvey writes the fixture and returns the expected answer.
func writeSurvey(dir string) ([]string, error) {
	if err := writeModule(dir, "survey"); err != nil {
		return nil, err
	}
	for p := 1; p <= surveyPackages; p++ {
		pkg := fmt.Sprintf("pkg%02d", p)
		if err := os.MkdirAll(filepath.Join(dir, pkg), 0o755); err != nil {
			return nil, err
		}
		for f := 1; f <= surveyFiles; f++ {
			var b strings.Builder
			fmt.Fprintf(&b, "package %s\n\n", pkg)
			for i := 1; i <= surveyFuncs; i++ {
				name := surveyName(f, i)
				// Unexported helpers never need a doc comment; a
				// comment separated by a blank line doesn't count.
				fmt.Fprintf(&b, "func helper%d%d(n int) int {\n\treturn n*%d + %d\n}\n\n", f, i, i, f)
				switch {
				case surveyUndocumented(p, f, i) && i%2 == 0:
					fmt.Fprintf(&b, "// section %d\n\n", i)
				case !surveyUndocumented(p, f, i):
					fmt.Fprintf(&b, "// %s returns a value derived from n for package %s.\n", name, pkg)
				}
				fmt.Fprintf(&b, "func %s(n int) int {\n\ttotal := 0\n\tfor k := 0; k < n; k++ {\n\t\ttotal += helper%d%d(k)\n\t}\n\treturn total\n}\n\n", name, f, i)
			}
			if err := writeFile(filepath.Join(dir, pkg), fmt.Sprintf("file%d.go", f), b.String()); err != nil {
				return nil, err
			}
		}
	}
	return surveyAnswer(), nil
}
