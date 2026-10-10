package tui

import (
	"strings"
	"testing"

	"charm.land/glamour/v2"
)

var markdownBenchmarkResult string

func BenchmarkRenderMarkdownWidth(b *testing.B) {
	const paragraph = "Larik keeps the conversation readable while the model streams useful detail and code examples. "
	markdown := "# Performance notes\n\n" + strings.Repeat(paragraph, 24) + "\n\n```go\nfunc render(width int) string { return strings.Repeat(\"line\\n\", width) }\n```\n\n| Path | Cost |\n| --- | ---: |\n| startup | low |\n| resize | repeated |\n"
	m := &model{isDark: true}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		markdownBenchmarkResult = m.renderMarkdownWidth(markdown, 100)
	}
}

func BenchmarkRewrapMarkdownHistory(b *testing.B) {
	const paragraph = "Larik keeps the conversation readable while the model streams useful detail and code examples. "
	markdown := "## Assistant response\n\n" + strings.Repeat(paragraph, 8) + "\n\n```go\nfunc render(width int) string { return strings.Repeat(\"line\\n\", width) }\n```"
	chunks := make([]string, 24)
	for i := range chunks {
		chunks[i] = markdown
	}
	m := &model{isDark: true}
	for _, tc := range []struct {
		name   string
		render func(string, int) string
	}{
		{name: "uncached", render: func(s string, width int) string { return renderMarkdownWidthUncached(m, s, width) }},
		{name: "cached", render: m.renderMarkdownWidth},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				var result strings.Builder
				for _, chunk := range chunks {
					result.WriteString(tc.render(chunk, 100))
				}
				markdownBenchmarkResult = result.String()
			}
		})
	}
}

func BenchmarkRepeatedConversationRewrap(b *testing.B) {
	const paragraph = "Larik keeps the conversation readable while the model streams useful detail and code examples. "
	markdown := "## Assistant response\n\n" + strings.Repeat(paragraph, 8) + "\n\n```go\nfunc render(width int) string { return strings.Repeat(\"line\\n\", width) }\n```"
	for _, cached := range []bool{false, true} {
		name := "uncached"
		if cached {
			name = "cached"
		}
		b.Run(name, func(b *testing.B) {
			m := &model{isDark: true}
			m.outputs = make([]conversationOutput, 24)
			for i := range m.outputs {
				source := markdown
				m.outputs[i] = conversationOutput{
					text:       source,
					sourceSize: len(source),
					render: func(m *model, width int) string {
						if cached {
							return m.renderMarkdownWidth(source, width)
						}
						return renderMarkdownWidthUncached(m, source, width)
					},
				}
			}
			if cached {
				for _, width := range []int{100, 110} {
					for i := range m.outputs {
						m.outputs[i].renderAt(m, width)
					}
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			var lines []string
			for range b.N {
				for _, width := range []int{100, 110, 100} {
					lines = lines[:0]
					for i := range m.outputs {
						if cached {
							m.outputs[i].renderAt(m, width)
						} else {
							m.outputs[i].text = m.outputs[i].render(m, width)
						}
						m.outputs[i].place(&lines, width)
					}
				}
				markdownBenchmarkResult = strings.Join(lines, "\n")
			}
		})
	}
}

// renderMarkdownWidthUncached preserves the renderer setup performed before
// the model-level renderer cache was added, for an apples-to-apples benchmark.
func renderMarkdownWidthUncached(m *model, s string, width int) string {
	renderer, err := glamour.NewTermRenderer(glamour.WithStyles(markdownStyleWithPalette(m.isDark, m.themePalette)), glamour.WithWordWrap(max(width-6, 20)))
	if err != nil {
		return s
	}
	var b strings.Builder
	var previous int
	for i, seg := range splitMarkdown(s) {
		r, err := renderer.Render(seg.text)
		if err != nil {
			return s
		}
		if i > 0 {
			if previous != mdProse || seg.kind != mdProse {
				b.WriteString("\n\n")
			} else {
				b.WriteString("\n")
			}
		}
		b.WriteString(frameMarkdownTables(trimBlankLines(r)))
		previous = seg.kind
	}
	return b.String()
}
