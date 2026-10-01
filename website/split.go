package main

import (
	"fmt"
	"regexp"
	"strings"
)

// The user guide is the README, cut at its `## ` headings (and at the
// `### ` headings listed in ownPage). Nothing is copied, so the site can't
// drift from the README.

// guideSlugs overrides the slug a README section would otherwise get.
var guideSlugs = map[string]string{
	"Quick start":                    "getting-started",
	"Mixing cheap and strong models": "model-routing",
	"Keys and commands":              "commands",
	"MCP servers":                    "mcp",
	"Language servers (LSP)":         "lsp",
}

// guideTitles overrides the title shown for a section.
var guideTitles = map[string]string{
	"Quick start":                    "Getting started",
	"Mixing cheap and strong models": "Model routing",
	"Keys and commands":              "Commands and keys",
}

// ownPage lists `### ` sections that become pages of their own.
var ownPage = map[string]bool{"Mixing cheap and strong models": true}

// Contributor sections stay in the repository README instead of the user guide.
var skipSections = map[string]bool{"Architecture": true, "Development": true}

type readmePart struct {
	Heading string // the README heading text ("" for the intro)
	Slug    string
	Title   string
	Body    string // Markdown without the heading line
}

var fenceRe = regexp.MustCompile("^\\s*(```|~~~)")

func splitReadme(src string) ([]readmePart, error) {
	var parts []readmePart
	cur := &readmePart{}
	inFence := false
	flush := func() {
		if cur.Heading != "" || strings.TrimSpace(cur.Body) != "" {
			parts = append(parts, *cur)
		}
	}
	for _, line := range strings.SplitAfter(src, "\n") {
		if fenceRe.MatchString(line) {
			inFence = !inFence
		}
		trim := strings.TrimRight(line, "\r\n")
		if !inFence {
			if strings.HasPrefix(trim, "# ") && cur.Heading == "" && cur.Body == "" {
				continue // the document title
			}
			h, isH2 := strings.CutPrefix(trim, "## ")
			h3, isH3 := strings.CutPrefix(trim, "### ")
			if isH3 && ownPage[h3] {
				h, isH2 = h3, true
			}
			if isH2 {
				flush()
				cur = &readmePart{Heading: h, Slug: slugify(h), Title: h}
				if s, ok := guideSlugs[h]; ok {
					cur.Slug = s
				}
				if t, ok := guideTitles[h]; ok {
					cur.Title = t
				}
				continue
			}
		}
		cur.Body += line
	}
	flush()
	if len(parts) == 0 || parts[0].Heading != "" {
		return nil, fmt.Errorf("README.md: expected an introduction before the first section")
	}
	var out []readmePart
	for _, p := range parts {
		if !skipSections[p.Heading] {
			out = append(out, p)
		}
	}
	return out, nil
}
