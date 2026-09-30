package skills

import (
	"embed"
	"path"
	"sort"
)

// Built-in commands ship inside the binary as command files, in the same
// format as a custom command. A command or skill of the same name found on
// disk replaces one.

//go:embed builtin/*.md
var builtinFS embed.FS

// ChangesMarker, in a built-in command's body, is where Larik puts the
// changes under review (see agent.reviewChanges).
const ChangesMarker = "{{CHANGES}}"

func builtins() []Skill {
	entries, _ := builtinFS.ReadDir("builtin")
	var out []Skill
	for _, e := range entries {
		file := path.Join("builtin", e.Name())
		data, err := builtinFS.ReadFile(file)
		if err != nil {
			continue
		}
		sk, err := parseCommandData(file, data, "built-in")
		if err != nil {
			continue
		}
		sk.Builtin, sk.data, sk.Dir = true, data, ""
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
