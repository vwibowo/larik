package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
)

type Download struct {
	OS     string `json:"os"`   // macos, linux, windows
	Arch   string `json:"arch"` // arm64, amd64
	Label  string `json:"label"`
	Detail string `json:"detail"`
	File   string `json:"file"`
	URL    string `json:"url"` // empty until a release is published
}

type Site struct {
	Name        string     `json:"name"`
	Tagline     string     `json:"tagline"`
	Description string     `json:"description"`
	Version     string     `json:"version"`
	GoVersion   string     `json:"go_version"`
	Downloads   []Download `json:"downloads"`
}

// DownloadsAvailable reports whether at least one platform has a live URL.
func (s Site) DownloadsAvailable() bool {
	for _, d := range s.Downloads {
		if d.URL != "" {
			return true
		}
	}
	return false
}

type Page struct {
	Kind    string // landing, doc, download, changelog, notfound
	Section string // Guide or Internals, for docs
	Title   string
	Nav     string // shorter sidebar label
	Desc    string
	URL     string // root-relative, e.g. /docs/sandbox/
	Source  string // repository-relative source file
	Anchor  string // README heading this page starts at, for README anchors
	Body    template.HTML
	TOC     []heading
	Mermaid bool

	Prev, Next *Page
	doc        *doc
}

type NavGroup struct {
	Title string
	Pages []*Page
}

type builder struct {
	root, dir, out, base string
	md                   goldmark.Markdown
	site                 Site
	pages                []*Page
	docs                 []*Page          // docs pages in sidebar order
	bySource             map[string]*Page // repo-relative source → page (first page for README.md)
	readmeAnchors        map[string]string
	nav                  []NavGroup
}

// build renders the site from root (the repository) and dir (website/)
// into out.
func build(root, dir, out, base string) error {
	b := &builder{root: root, dir: dir, out: out, base: strings.TrimSuffix(base, "/"), md: newMarkdown(),
		bySource: map[string]*Page{}, readmeAnchors: map[string]string{}}
	raw, err := os.ReadFile(filepath.Join(dir, "site.json"))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &b.site); err != nil {
		return fmt.Errorf("site.json: %w", err)
	}
	if err := b.loadGuide(); err != nil {
		return err
	}
	if err := b.loadInternals(); err != nil {
		return err
	}
	if err := b.loadChangelog(); err != nil {
		return err
	}
	b.pages = append(b.pages,
		&Page{Kind: "landing", Title: b.site.Name + " — " + b.site.Tagline, Desc: b.site.Description, URL: "/"},
		&Page{Kind: "download", Title: "Download", Desc: "Install " + b.site.Name + " on macOS, Linux or Windows, or build it from source.", URL: "/download/"},
		&Page{Kind: "notfound", Title: "Page not found", URL: "/404.html"},
	)
	for i, p := range b.docs {
		if i > 0 {
			p.Prev = b.docs[i-1]
		}
		if i < len(b.docs)-1 {
			p.Next = b.docs[i+1]
		}
	}
	if err := b.renderMarkdown(); err != nil {
		return err
	}
	return b.write()
}

func (b *builder) addDoc(p *Page, src []byte) {
	p.doc = parseDoc(b.md, src)
	p.Mermaid = p.doc.mermaid
	p.Desc = firstParagraph(p.doc)
	// The TOC shows the top two heading levels a page uses, numbered from 1.
	top := 6
	for _, h := range p.doc.headings {
		top = min(top, h.Level)
	}
	for _, h := range p.doc.headings {
		if h.Level <= top+1 {
			h.Level -= top - 1
			p.TOC = append(p.TOC, h)
		}
	}
	if p.Title == "" {
		p.Title = p.doc.title
	}
	if p.Nav == "" {
		p.Nav = p.Title
	}
	if _, ok := b.bySource[p.Source]; !ok {
		b.bySource[p.Source] = p
	}
	b.pages = append(b.pages, p)
	if p.Kind == "doc" {
		b.docs = append(b.docs, p)
	}
}

func (b *builder) loadGuide() error {
	raw, err := os.ReadFile(filepath.Join(b.root, "README.md"))
	if err != nil {
		return err
	}
	parts, err := splitReadme(string(raw))
	if err != nil {
		return err
	}
	group := NavGroup{Title: "Guide"}
	for i, part := range parts {
		p := &Page{Kind: "doc", Section: "Guide", Title: part.Title, Source: "README.md", Anchor: part.Heading,
			URL: "/docs/" + part.Slug + "/"}
		if i == 0 {
			p.Title, p.URL = "Introduction", "/docs/"
		}
		b.addDoc(p, []byte(part.Body))
		if part.Heading != "" {
			b.readmeAnchors[slugify(part.Heading)] = p.URL
		}
		for _, h := range p.doc.headings {
			if _, dup := b.readmeAnchors[h.ID]; !dup {
				b.readmeAnchors[h.ID] = p.URL + "#" + h.ID
			}
		}
		group.Pages = append(group.Pages, p)
	}
	b.nav = append(b.nav, group)
	return nil
}

// readingOrder matches rows of the table in docs/README.md:
// | 1 | [Architecture](architecture.md) | ... |
var readingOrder = regexp.MustCompile(`(?m)^\|\s*\d+\s*\|\s*\[([^\]]+)\]\(([\w.-]+\.md)\)`)

func (b *builder) loadInternals() error {
	docsDir := filepath.Join(b.root, "docs")
	index, err := os.ReadFile(filepath.Join(docsDir, "README.md"))
	if err != nil {
		return err
	}
	group := NavGroup{Title: "Internals"}
	overview := &Page{Kind: "doc", Section: "Internals", Nav: "Overview", Source: "docs/README.md", URL: "/docs/internals/"}
	b.addDoc(overview, index)
	group.Pages = append(group.Pages, overview)

	seen := map[string]bool{"README.md": true}
	add := func(file, nav string) error {
		src, err := os.ReadFile(filepath.Join(docsDir, file))
		if err != nil {
			return err
		}
		seen[file] = true
		p := &Page{Kind: "doc", Section: "Internals", Nav: nav, Source: "docs/" + file,
			URL: "/docs/internals/" + strings.TrimSuffix(file, ".md") + "/"}
		b.addDoc(p, src)
		group.Pages = append(group.Pages, p)
		return nil
	}
	for _, m := range readingOrder.FindAllStringSubmatch(string(index), -1) {
		if err := add(m[2], m[1]); err != nil {
			return err
		}
	}
	// Docs missing from the reading order still get a page.
	entries, err := os.ReadDir(docsDir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".md") && !seen[e.Name()] {
			if err := add(e.Name(), ""); err != nil {
				return err
			}
		}
	}
	b.nav = append(b.nav, group)
	return nil
}

func (b *builder) loadChangelog() error {
	src, err := os.ReadFile(filepath.Join(b.dir, "content", "changelog.md"))
	if err != nil {
		return err
	}
	b.addDoc(&Page{Kind: "changelog", Source: "website/content/changelog.md", URL: "/changelog/"}, src)
	return nil
}

var lineSuffix = regexp.MustCompile(`:\d+$`)

// resolve maps a link in p's source to a site URL. Links to Markdown that
// became pages are rewritten; links to other repository files are
// unwrapped; anything else that doesn't resolve fails the build.
func (b *builder) resolve(p *Page, dest string) (string, bool, error) {
	if strings.Contains(dest, "://") || strings.HasPrefix(dest, "mailto:") {
		return dest, false, nil
	}
	file, frag, _ := strings.Cut(dest, "#")
	if strings.HasPrefix(file, "/") { // a site path, used by the changelog
		return b.url(dest), false, nil
	}
	var target *Page
	if file == "" {
		target = p
	} else {
		rel := path.Clean(path.Join(path.Dir(p.Source), file))
		target = b.bySource[rel]
		if target == nil {
			if _, err := os.Stat(filepath.Join(b.root, filepath.FromSlash(lineSuffix.ReplaceAllString(rel, "")))); err == nil {
				return "", true, nil
			}
			return "", false, fmt.Errorf("%s: link to %q: no such page or file", p.Source, dest)
		}
	}
	if target.Source == "README.md" {
		if frag == "" {
			return b.url(target.URL), false, nil
		}
		u, ok := b.readmeAnchors[frag]
		if !ok {
			return "", false, fmt.Errorf("%s: link to %q: no heading #%s in README.md", p.Source, dest, frag)
		}
		return b.url(u), false, nil
	}
	if frag == "" {
		return b.url(target.URL), false, nil
	}
	if !target.doc.ids[frag] {
		return "", false, fmt.Errorf("%s: link to %q: no heading #%s in %s", p.Source, dest, frag, target.Source)
	}
	if target == p {
		return "#" + frag, false, nil
	}
	return b.url(target.URL) + "#" + frag, false, nil
}

func (b *builder) url(p string) string { return b.base + p }

func (b *builder) renderMarkdown() error {
	var errs []string
	for _, p := range b.pages {
		if p.doc == nil {
			continue
		}
		if err := p.doc.rewriteLinks(func(dest string) (string, bool, error) { return b.resolve(p, dest) }); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		html, err := p.doc.render(b.md)
		if err != nil {
			return fmt.Errorf("%s: %w", p.Source, err)
		}
		p.Body = template.HTML(html)
	}
	if len(errs) > 0 {
		return fmt.Errorf("broken links:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func firstParagraph(d *doc) string {
	for n := d.root.FirstChild(); n != nil; n = n.NextSibling() {
		if para, ok := n.(*ast.Paragraph); ok {
			t := strings.Join(strings.Fields(plainText(para, d.src)), " ")
			if len(t) > 180 {
				cut := strings.LastIndex(t[:177], " ")
				if cut < 0 {
					cut = 177
				}
				t = t[:cut] + "…"
			}
			return t
		}
	}
	return ""
}

type searchEntry struct {
	Title    string      `json:"t"`
	URL      string      `json:"u"`
	Section  string      `json:"s"`
	Headings [][2]string `json:"h"`
	Text     string      `json:"x"`
}

func (b *builder) write() error {
	if err := os.RemoveAll(b.out); err != nil {
		return err
	}
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"url": b.url,
		"code": func(lang, src string) (template.HTML, error) {
			h, err := codeHTML(lang, src)
			return template.HTML(h), err
		},
		"sub": func(a, b int) int { return a - b },
	}).ParseGlob(filepath.Join(b.dir, "templates", "*.html"))
	if err != nil {
		return err
	}
	for _, p := range b.pages {
		file := filepath.Join(b.out, filepath.FromSlash(p.URL))
		if strings.HasSuffix(p.URL, "/") {
			file = filepath.Join(file, "index.html")
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		f, err := os.Create(file)
		if err != nil {
			return err
		}
		err = tmpl.ExecuteTemplate(f, "base", map[string]any{"Site": b.site, "Page": p, "Nav": b.nav, "Base": b.base})
		f.Close()
		if err != nil {
			return fmt.Errorf("%s: %w", p.URL, err)
		}
	}

	var index []searchEntry
	for _, p := range b.docs {
		e := searchEntry{Title: p.Title, URL: b.url(p.URL), Section: p.Section, Headings: [][2]string{}, Text: p.doc.text()}
		for _, h := range p.TOC {
			e.Headings = append(e.Headings, [2]string{h.ID, h.Text})
		}
		index = append(index, e)
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(b.out, "search-index.json"), raw, 0o644); err != nil {
		return err
	}

	css, err := chromaCSS()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(b.out, "css"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(b.out, "css", "chroma.css"), []byte(css), 0o644); err != nil {
		return err
	}
	return copyDir(filepath.Join(b.dir, "static"), b.out)
}

func copyDir(src, dst string) error {
	var files []string
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		return err
	}
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(src, f)
		raw, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(to, raw, 0o644); err != nil {
			return err
		}
	}
	return nil
}
