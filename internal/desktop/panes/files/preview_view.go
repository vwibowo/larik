package filespane

import (
	"fmt"
	"path/filepath"
	"runtime"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

type PreviewViewOptions struct {
	State        *State
	Opacity      float32
	BackIcon     *ui.SVG
	WrapIcon     *ui.SVG
	ExternalIcon *ui.SVG
	FolderIcon   *ui.SVG
	IconButton   func(string, *ui.SVG, string, bool, func())
	OpenPath     func(string)
	ShowFile     func(string)
}

func PreviewView(c *ui.Context, theme *ui.Theme, v PreviewViewOptions) {
	f := v.State
	ui.Column(c).Fill().MinWidth(0).Children(func() {
		ui.Row(c).Height(48).Gap(7).Padding(0, 9, 0, 12).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			if f.Narrow {
				v.IconButton("back-to-files", v.BackIcon, "Back to Files", false, func() { f.PreviewOnly = false })
			}
			name := "Preview"
			if f.Selected != "" {
				name = filepath.Base(f.Selected)
			}
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, name).FontSize(13).Bold().SingleLine().Shrink(1).MinWidth(0).Tooltip(f.Selected)
				if f.Selected != "" {
					parent, _ := filepath.Rel(f.Root, filepath.Dir(f.Selected))
					if parent == "." {
						parent = "Project root"
					}
					meta := parent
					if f.Preview.Path != "" && !f.Preview.Loading {
						meta += " · " + SizeLabel(f.Preview.Size)
					}
					ui.Text(c, meta).FontSize(10).TextColor(theme.TextMuted).SingleLine().Shrink(1).MinWidth(0).Tooltip(meta)
				}
			})
			if f.Preview.Path != "" {
				if f.Preview.Code != nil {
					v.IconButton("wrap-lines", v.WrapIcon, "Wrap lines", f.Wrap, func() {
						f.Wrap = !f.Wrap
						f.Preview.Code.SoftWrap = f.Wrap
					})
				}
				v.IconButton("open-external", v.ExternalIcon, "Open externally", false, func() {
					if v.OpenPath != nil {
						v.OpenPath(f.Selected)
					}
				})
				v.IconButton("reveal-file", v.FolderIcon, RevealLabel(), false, func() {
					if v.ShowFile != nil {
						v.ShowFile(f.Selected)
					}
				})
			}
		})
		if f.Selected == "" {
			ui.Column(c).Grow(1).Center().Gap(7).Children(func() {
				ui.Icon(c, v.FolderIcon).FontSize(28).TextColor(theme.TextMuted)
				ui.Text(c, "Select a file to preview").FontSize(13).TextColor(theme.TextMuted)
			})
			return
		}
		p := f.Preview
		switch {
		case p.Path == "":
			ui.Text(c, "Choose a file in this folder to preview it.").FontSize(12).TextColor(theme.TextMuted).Margin(ui.Auto)
		case p.Note != "":
			ui.Text(c, p.Note).FontSize(12).TextColor(theme.TextMuted).Padding(14)
		case p.Image != nil:
			ui.Image(c, p.Image).Grow(1).MinHeight(0).Fit(ui.Contain).Padding(12)
		case p.Web != nil:
			ui.WebView(c.Key("pdf-preview"), p.Web).Grow(1).MinWidth(0).MinHeight(0).Children(func() {
				if p.Loading {
					ui.Text(c, "Loading preview…").FontSize(12).Padding(6, 10).Radius(6).Background(theme.Background).Absolute().Top(12).Left(12)
				}
			})
		case p.Loading:
			ui.Text(c, "Loading preview…").FontSize(12).TextColor(theme.TextMuted).Margin(ui.Auto)
		case p.Code != nil:
			codeTheme := editor.DefaultTheme(theme)
			if v.Opacity < 1 {
				codeTheme.Background = theme.Background.Alpha(0)
			}
			p.Code.Theme = codeTheme
			editor.View(c.Key("code-preview"), p.Code).Grow(1).MinWidth(0).MinHeight(0)
		default:
			ui.Text(c, "This file cannot be previewed.").FontSize(12).TextColor(theme.TextMuted).Margin(ui.Auto)
		}
	})
}

func SizeLabel(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	if size < 1<<20 {
		return fmt.Sprintf("%.1f KB", float64(size)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
}

func RevealLabel() string {
	if runtime.GOOS == "darwin" {
		return "Show in Finder"
	}
	return "Show in file manager"
}
