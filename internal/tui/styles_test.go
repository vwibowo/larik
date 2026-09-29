package tui

import (
	"fmt"

	"larik/internal/llm"
	"larik/internal/permission"
	"testing"

	"charm.land/glamour/v2/ansi"
	"charm.land/lipgloss/v2"
)

// The TUI leaves the terminal's own background showing: only the diff tints
// may set one.
func TestThemeBackgroundsAreTransparent(t *testing.T) {
	for _, dark := range []bool{true, false} {
		st := newStyles(dark)
		for name, s := range map[string]lipgloss.Style{
			"accent": st.accent, "dim": st.dim, "user": st.user, "box": st.box, "modal": st.modal,
			"statusMode": st.statusMode, "chip": st.chip, "chipWarn": st.chipWarn, "chipPlain": st.chipPlain,
		} {
			if bg := s.GetBackground(); bg != (lipgloss.NoColor{}) {
				t.Errorf("dark=%v %s has background %v", dark, name, bg)
			}
		}
		if bg := transparentInput(dark).Focused.CursorLine.GetBackground(); bg != (lipgloss.NoColor{}) {
			t.Errorf("dark=%v input cursor line has background %v", dark, bg)
		}

		cfg := markdownStyle(dark)
		blocks := map[string]ansi.StylePrimitive{
			"document": cfg.Document.StylePrimitive, "heading": cfg.Heading.StylePrimitive,
			"h1": cfg.H1.StylePrimitive, "h2": cfg.H2.StylePrimitive, "h3": cfg.H3.StylePrimitive,
			"h4": cfg.H4.StylePrimitive, "h5": cfg.H5.StylePrimitive, "h6": cfg.H6.StylePrimitive,
			"code": cfg.Code.StylePrimitive, "codeblock": cfg.CodeBlock.StylePrimitive,
		}
		if cfg.CodeBlock.Chroma != nil {
			blocks["chroma background"] = cfg.CodeBlock.Chroma.Background
			blocks["chroma error"] = cfg.CodeBlock.Chroma.Error
		}
		for name, p := range blocks {
			if p.BackgroundColor != nil {
				t.Errorf("dark=%v markdown %s has background %s", dark, name, *p.BackgroundColor)
			}
		}
	}
}

func fg(s lipgloss.Style) string { return fmt.Sprint(s.GetForeground()) }

// Modes and effort levels must be told apart by color alone.
func TestModeAndEffortColorsAreDistinct(t *testing.T) {
	for _, dark := range []bool{true, false} {
		st := newStyles(dark)
		seen := map[string]string{}
		for name, s := range map[string]lipgloss.Style{
			"default": st.modeStyle(permission.ModeDefault), "accept": st.modeStyle(permission.ModeAcceptEdits),
			"plan": st.modeStyle(permission.ModePlan), "yolo": st.modeStyle(permission.ModeYolo),
			"accent": st.accent, "warn": st.warn,
		} {
			c := fg(s)
			if prev, ok := seen[c]; ok {
				t.Errorf("dark=%v %s and %s share color %s", dark, name, prev, c)
			}
			seen[c] = name
			if bg := s.GetBackground(); bg != (lipgloss.NoColor{}) {
				t.Errorf("dark=%v %s has background %v", dark, name, bg)
			}
		}
		levels := []llm.Effort{llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortXHigh, llm.EffortMax}
		ramp := map[string]llm.Effort{}
		marks := map[string]bool{}
		for _, e := range levels {
			s, mark := st.effortStyle(e)
			if mark == "" || marks[mark] {
				t.Errorf("dark=%v effort %s has missing or repeated bar %q", dark, e, mark)
			}
			marks[mark] = true
			if prev, ok := ramp[fg(s)]; ok {
				t.Errorf("dark=%v effort %s and %s share color", dark, e, prev)
			}
			ramp[fg(s)] = e
		}
		if _, mark := st.effortStyle(llm.EffortDefault); mark != "" {
			t.Errorf("default effort should have no bar, got %q", mark)
		}
	}
}
