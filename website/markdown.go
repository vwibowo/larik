package main

import (
	"bufio"
	"bytes"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"
	"unicode"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// codeStyle is the chroma style whose classes site.css builds on.
const codeStyle = "github-dark"

type heading struct {
	Level    int
	ID, Text string
}

// doc is one parsed Markdown source. Links are rewritten on the AST
// between parsing and rendering, once every page's heading IDs are known.
type doc struct {
	src      []byte
	root     ast.Node
	title    string // first H1, removed from the body
	headings []heading
	ids      map[string]bool
	mermaid  bool
}

func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(
			gmhtml.WithUnsafe(),
			renderer.WithNodeRenderers(util.Prioritized(codeRenderer{}, 100)),
		),
	)
}

func parseDoc(md goldmark.Markdown, src []byte) *doc {
	ctx := parser.NewContext(parser.WithIDs(&githubIDs{seen: map[string]int{}}))
	root := md.Parser().Parse(text.NewReader(src), parser.WithContext(ctx))
	d := &doc{src: src, root: root, ids: map[string]bool{}}
	var drop ast.Node
	_ = ast.Walk(root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Heading:
			t := plainText(n, src)
			if n.Level == 1 && d.title == "" {
				d.title, drop = t, n
				return ast.WalkSkipChildren, nil
			}
			id := ""
			if v, ok := n.AttributeString("id"); ok {
				id = string(v.([]byte))
			}
			d.ids[id] = true
			d.headings = append(d.headings, heading{n.Level, id, t})
			return ast.WalkSkipChildren, nil
		case *ast.FencedCodeBlock:
			if string(n.Language(src)) == "mermaid" {
				d.mermaid = true
			}
		}
		return ast.WalkContinue, nil
	})
	if drop != nil {
		drop.Parent().RemoveChild(drop.Parent(), drop)
	}
	return d
}

// rewriteLinks maps every link destination through resolve. A resolver
// that returns unwrap replaces the link with its text (links to source
// files, which have no page on the site).
func (d *doc) rewriteLinks(resolve func(dest string) (string, bool, error)) error {
	var unwrap []*ast.Link
	var errs []string
	_ = ast.Walk(d.root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		l, ok := n.(*ast.Link)
		if !ok || !entering {
			return ast.WalkContinue, nil
		}
		dest, drop, err := resolve(string(l.Destination))
		switch {
		case err != nil:
			errs = append(errs, err.Error())
		case drop:
			unwrap = append(unwrap, l)
		default:
			l.Destination = []byte(dest)
		}
		return ast.WalkContinue, nil
	})
	for _, l := range unwrap {
		p := l.Parent()
		for c := l.FirstChild(); c != nil; {
			next := c.NextSibling()
			p.InsertBefore(p, l, c)
			c = next
		}
		p.RemoveChild(p, l)
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n  "))
	}
	return nil
}

var (
	headingRe = regexp.MustCompile(`<h([2-4]) id="([^"]+)">`)
)

func (d *doc) render(md goldmark.Markdown) (string, error) {
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, d.src, d.root); err != nil {
		return "", err
	}
	out := buf.String()
	out = strings.ReplaceAll(out, "<table>", `<div class="table-wrap"><table>`)
	out = strings.ReplaceAll(out, "</table>", "</table></div>")
	out = headingRe.ReplaceAllString(out, `<h$1 id="$2"><a class="anchor" href="#$2" aria-hidden="true" tabindex="-1">#</a>`)
	return out, nil
}

// text is the page's plain text, for the search index.
func (d *doc) text() string {
	var b strings.Builder
	_ = ast.Walk(d.root, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			if n.Type() == ast.TypeBlock {
				b.WriteByte(' ')
			}
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Text:
			b.Write(n.Segment.Value(d.src))
			if n.SoftLineBreak() || n.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(n.Value)
		case *ast.FencedCodeBlock:
			if string(n.Language(d.src)) == "mermaid" {
				return ast.WalkSkipChildren, nil
			}
			lines := n.Lines()
			for i := 0; i < lines.Len(); i++ {
				s := lines.At(i)
				b.Write(s.Value(d.src))
			}
		}
		return ast.WalkContinue, nil
	})
	return strings.Join(strings.Fields(b.String()), " ")
}

func plainText(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Text:
			b.Write(n.Segment.Value(src))
			if n.SoftLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(n.Value)
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// githubIDs generates heading IDs the way GitHub does, so anchors written
// for the repository's Markdown keep working on the site.
type githubIDs struct{ seen map[string]int }

func (g *githubIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	id := slugify(string(value))
	if n, ok := g.seen[id]; ok {
		g.seen[id] = n + 1
		id = fmt.Sprintf("%s-%d", id, n+1)
	} else {
		g.seen[id] = 0
	}
	return []byte(id)
}

func (g *githubIDs) Put(value []byte) { g.seen[string(value)] = 0 }

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// codeRenderer highlights fenced code with chroma CSS classes, adds a
// language label and copy button, and hands mermaid blocks to the browser.
type codeRenderer struct{}

func (codeRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindFencedCodeBlock, renderCode)
	reg.Register(ast.KindCodeBlock, renderCode)
}

var langLabels = map[string]string{"": "text", "bash": "shell", "sh": "shell", "zsh": "shell", "console": "shell"}

func renderCode(w util.BufWriter, src []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	lang := ""
	if f, ok := n.(*ast.FencedCodeBlock); ok {
		lang = string(f.Language(src))
	}
	var code strings.Builder
	lines := n.Lines()
	for i := 0; i < lines.Len(); i++ {
		s := lines.At(i)
		code.Write(s.Value(src))
	}
	if lang == "mermaid" {
		fmt.Fprintf(w, `<figure class="diagram"><pre class="mermaid">%s</pre></figure>`, html.EscapeString(code.String()))
		return ast.WalkSkipChildren, nil
	}
	label, ok := langLabels[lang]
	if !ok {
		label = lang
	}
	fmt.Fprintf(w, `<div class="code"><div class="code-bar"><span>%s</span><button class="copy" type="button">Copy</button></div><pre class="chroma"><code>`, html.EscapeString(label))
	if err := highlight(w, lang, code.String()); err != nil {
		return ast.WalkStop, err
	}
	_, _ = w.WriteString("</code></pre></div>\n")
	return ast.WalkSkipChildren, nil
}

// codeHTML renders a code block outside Markdown, for templates.
func codeHTML(lang, src string) (string, error) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	label, ok := langLabels[lang]
	if !ok {
		label = lang
	}
	fmt.Fprintf(w, `<div class="code"><div class="code-bar"><span>%s</span><button class="copy" type="button">Copy</button></div><pre class="chroma"><code>`, html.EscapeString(label))
	if err := highlight(w, lang, strings.TrimLeft(src, "\n")); err != nil {
		return "", err
	}
	_, _ = w.WriteString("</code></pre></div>")
	err := w.Flush()
	return buf.String(), err
}

func highlight(w io.Writer, lang, code string) error {
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	it, err := chroma.Coalesce(lexer).Tokenise(nil, code)
	if err != nil {
		return err
	}
	f := chromahtml.New(chromahtml.WithClasses(true), chromahtml.PreventSurroundingPre(true))
	return f.Format(w, styles.Get(codeStyle), it)
}

// chromaCSS returns the stylesheet for the classes highlight emits.
func chromaCSS() (string, error) {
	var buf bytes.Buffer
	f := chromahtml.New(chromahtml.WithClasses(true))
	if err := f.WriteCSS(&buf, styles.Get(codeStyle)); err != nil {
		return "", err
	}
	return buf.String(), nil
}
