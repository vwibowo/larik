package tui

import (
	"charm.land/glamour/v2/ansi"

	glamourstyles "charm.land/glamour/v2/styles"
	"larik/internal/config"
)

// markdownStyle is glamour's dark or light style recolored to the TUI palette
// with every background removed, so rendered Markdown sits on the terminal's
// own background instead of glamour's heading and code-block fills.
func markdownStyle(isDark bool) ansi.StyleConfig { return markdownStyleWithPalette(isDark, nil) }

func markdownStyleWithPalette(isDark bool, p *config.ThemePalette) ansi.StyleConfig {
	cfg := glamourstyles.LightStyleConfig
	text, dim, accent := "#1F1F24", "#6B6B76", "#0D9488"
	if isDark {
		cfg = glamourstyles.DarkStyleConfig
		text, dim, accent = "#ECECF0", "#7D7E87", "#2DD4BF"
	}
	if p != nil {
		if p.Foreground != "" {
			text = p.Foreground
		}
		if p.ANSI[8] != "" {
			dim = p.ANSI[8]
		}
		if p.ANSI[14] != "" {
			accent = p.ANSI[14]
		}
	}
	ptr := func(s string) *string { return &s }
	flat := func(p ansi.StylePrimitive, color string) ansi.StylePrimitive {
		p.Color, p.BackgroundColor = ptr(color), nil
		return p
	}

	cfg.Document.StylePrimitive = flat(cfg.Document.StylePrimitive, text)
	for _, h := range []*ansi.StyleBlock{&cfg.Heading, &cfg.H1, &cfg.H2, &cfg.H3, &cfg.H4, &cfg.H5, &cfg.H6} {
		h.StylePrimitive = flat(h.StylePrimitive, accent)
	}
	// glamour pads H1 with spaces to sit inside its fill; without the fill they are just gaps.
	cfg.H1.Prefix, cfg.H1.Suffix = "", ""
	cfg.HorizontalRule = flat(cfg.HorizontalRule, dim)
	cfg.Link = flat(cfg.Link, accent)
	cfg.LinkText = flat(cfg.LinkText, accent)
	cfg.Image = flat(cfg.Image, accent)
	cfg.ImageText = flat(cfg.ImageText, dim)
	cfg.Item = flat(cfg.Item, accent)
	cfg.Enumeration = flat(cfg.Enumeration, accent)
	cfg.Code.StylePrimitive = flat(cfg.Code.StylePrimitive, accent)
	cfg.CodeBlock.StylePrimitive = flat(cfg.CodeBlock.StylePrimitive, dim)
	if cfg.CodeBlock.Chroma != nil {
		chroma := *cfg.CodeBlock.Chroma
		chroma.Background.BackgroundColor = nil
		chroma.Error.BackgroundColor = nil
		cfg.CodeBlock.Chroma = &chroma
	}
	return cfg
}
