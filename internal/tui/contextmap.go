package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"larik/internal/agent"
)

// /context draws what fills the context as a treemap of cells, so it works
// in every terminal and re-lays out with the conversation. The areas are
// estimates, which is why they show as shares and never as token counts.

const (
	contextMapRows    = 14
	contextMapMaxCols = 96
	// contextMapMinCols is the narrowest map worth drawing; a narrower
	// window keeps the list.
	contextMapMinCols = 24
	// contextMapMinShare is the percentage below which a part joins "other".
	contextMapMinShare = 2
)

// contextMapColors stay dark enough for white text on any theme.
var contextMapColors = []string{"#2f6f9f", "#8a4f9e", "#2f8a6f", "#a8552f", "#5a5fb0", "#8a7a2f", "#a03f62", "#2f7f8a"}

const contextMapOther = "#5f6368"

func (m *model) contextMapCommand() tea.Cmd {
	c := m.agent.ContextMap()
	return m.printlnRendered(256, func(m *model, width int) string {
		return m.renderContextMap(c, width)
	})
}

type contextBox struct {
	name, color string
	share       int
	weight      int
	rect        mapRect
}

// contextBoxes turns parts into what the map draws, folding small ones
// into a box that lists them.
func contextBoxes(c agent.ContextMap) (boxes []contextBox, small []string) {
	other := contextBox{name: "other", color: contextMapOther}
	for _, p := range c.Parts {
		share := p.Weight * 100 / max(c.Total, 1)
		if share < contextMapMinShare {
			other.weight += p.Weight
			small = append(small, p.Name)
			continue
		}
		color := contextMapColors[len(boxes)%len(contextMapColors)]
		boxes = append(boxes, contextBox{name: p.Name, color: color, share: share, weight: p.Weight})
	}
	if other.weight > 0 {
		other.share = other.weight * 100 / max(c.Total, 1)
		boxes = append(boxes, other)
	}
	return boxes, small
}

func (m *model) renderContextMap(c agent.ContextMap, width int) string {
	cols := min(width-2, contextMapMaxCols)
	var b []string
	if c.Tokens > 0 && c.Window > 0 {
		used := min(c.Tokens*100/c.Window, 100)
		bar := max(min(cols, 40), 1)
		full := used * bar / 100
		b = append(b, "  "+m.st.accent.Render(strings.Repeat("█", full))+m.st.dim.Render(strings.Repeat("░", bar-full))+
			fmt.Sprintf(" %d%% of %s", used, humanTokens(c.Window)))
	} else {
		b = append(b, m.st.dim.Render("  size not measured yet: it's known after the next request"))
	}
	boxes, small := contextBoxes(c)
	if cols >= contextMapMinCols {
		b = append(b, drawContextMap(boxes, cols)...)
	} else {
		for _, x := range boxes {
			b = append(b, fmt.Sprintf("  %3d%%  %s", x.share, x.name))
		}
	}
	if len(small) > 0 {
		b = append(b, m.st.dim.Render(ansi.Truncate("  other: "+strings.Join(small, ", "), max(width, 1), "…")))
	}
	if len(c.Largest) > 0 {
		b = append(b, "", "  Largest results")
		for _, it := range c.Largest {
			line := fmt.Sprintf("  %3d%%  %s", it.Weight*100/max(c.Total, 1), toolTitle(it.Tool, it.Input, m.shortPaths))
			b = append(b, ansi.Truncate(line, max(width, 1), "…"))
		}
	}
	b = append(b, "", m.st.dim.Render("  Shares are estimates. /compact summarizes the conversation."))
	return strings.Join(b, "\n")
}

// drawContextMap lays boxes out in cols by contextMapRows cells.
func drawContextMap(boxes []contextBox, cols int) []string {
	weights := make([]int, len(boxes))
	for i, x := range boxes {
		weights[i] = x.weight
	}
	rects := make([]mapRect, len(boxes))
	layoutMap(weights, rects, mapRect{0, 0, cols, contextMapRows})
	for i := range boxes {
		boxes[i].rect = rects[i]
	}
	lines := make([]string, contextMapRows)
	for row := range lines {
		line := "  "
		for x := 0; x < cols; {
			box := boxAt(boxes, x, row)
			if box == nil {
				line += " "
				x++
				continue
			}
			line += box.cells(row)
			x += box.rect.w
		}
		lines[row] = line
	}
	return lines
}

func boxAt(boxes []contextBox, x, y int) *contextBox {
	for i := range boxes {
		if r := boxes[i].rect; x == r.x && y >= r.y && y < r.y+r.h && r.w > 0 {
			return &boxes[i]
		}
	}
	return nil
}

// cells is the box's text on one row, as wide as the box with a gap on the
// right that keeps neighbours apart.
func (c *contextBox) cells(row int) string {
	inner := max(c.rect.w-1, 0)
	text := ""
	share := fmt.Sprintf("%d%%", c.share)
	switch {
	case c.rect.h == 1:
		text = c.name + " " + share
	case row == c.rect.y:
		text = c.name
	case row == c.rect.y+1:
		text = share
	}
	if inner > 2 {
		text = " " + ansi.Truncate(text, inner-1, "…")
	} else {
		text = ""
	}
	text += strings.Repeat(" ", max(inner-ansi.StringWidth(text), 0))
	style := lipgloss.NewStyle().Background(lipgloss.Color(c.color)).Foreground(lipgloss.Color("#ffffff"))
	return style.Render(text) + " "
}

type mapRect struct{ x, y, w, h int }

// layoutMap fills rects, parallel to weights (largest first), to tile r with
// areas that follow the weights: it halves the weight and cuts r across its
// longer side, counting a cell as twice as tall as wide.
func layoutMap(weights []int, rects []mapRect, r mapRect) {
	if len(weights) == 0 {
		return
	}
	if len(weights) == 1 {
		rects[0] = r
		return
	}
	total := 0
	for _, w := range weights {
		total += w
	}
	cut, left := 0, 0
	for cut < len(weights)-1 && (cut == 0 || left*2 < total) {
		left += weights[cut]
		cut++
	}
	a, b := r, r
	if r.w >= r.h*2 {
		a.w = min(max((r.w*left+total/2)/max(total, 1), 1), r.w-1)
		b.x, b.w = r.x+a.w, r.w-a.w
	} else {
		a.h = min(max((r.h*left+total/2)/max(total, 1), 1), max(r.h-1, 1))
		b.y, b.h = r.y+a.h, r.h-a.h
	}
	layoutMap(weights[:cut], rects[:cut], a)
	layoutMap(weights[cut:], rects[cut:], b)
}
