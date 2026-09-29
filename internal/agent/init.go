package agent

import (
	"fmt"
	"path/filepath"
	"strings"
)

// InitCommand is the built-in /init: study the repository and write its
// instruction file. It expands in the agent, so it works the same in the
// TUI, with -p and over the server.
const InitCommand = "/init"

// initPrompt is what /init sends: create the project's instruction file,
// or improve the one that exists. AGENTS.md is preferred; an existing
// CLAUDE.md is improved in place rather than shadowed by a new AGENTS.md.
func (a *Agent) initPrompt(extra string) string {
	root := GitRoot(a.opts.Cwd)
	if root == "" {
		root = a.opts.Cwd
	}
	target, verb := filepath.Join(root, "AGENTS.md"), "Create"
	for _, name := range instructionFiles {
		if p := filepath.Join(root, name); exists(p) {
			target, verb = p, "Improve"
			break
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: the instructions coding agents read before working in this repository.\n\n", verb, target)
	if verb == "Improve" {
		b.WriteString("It exists already. Read it first, keep what is still accurate, fix what the code contradicts, and add what is missing. Don't rewrite it for the sake of it.\n\n")
	}
	b.WriteString(`Explore before writing: the README, build and dependency files (go.mod, package.json, Cargo.toml, pyproject.toml, Makefile and the like), CI configuration, the top-level layout, and a few representative source files and tests. Rules files from other tools (.cursorrules, .cursor/rules, .github/copilot-instructions.md) are worth folding in.

Write what an agent can't work out quickly on its own:
- How to build, test (including running a single test), lint and format, as exact commands.
- The architecture in brief: the main packages or modules and how they fit together, where to find what.
- Conventions and invariants that are easy to break, and anything surprising about the setup.

Keep it short and specific to this repository, in Markdown with a few headings. Leave out generic advice ("write clean code", "add tests"), long file listings that ls would show, and anything you aren't sure of. When you're done, say in one line what you wrote, and that it applies from the next fresh context (/clear or a new session).`)
	if extra = strings.TrimSpace(extra); extra != "" {
		b.WriteString("\n\nThe user added: " + extra)
	}
	return b.String()
}
