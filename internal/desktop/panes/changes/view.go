package changespane

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"
)

const (
	CompareCommit = iota
	CompareBase
)

type ViewOptions struct {
	Icon                                                    *ui.SVG
	Root, Dir, Branch, Base, Busy, Error                    string
	LocationTooltip, CountLabel                             string
	Loaded, CanPullRequest                                  bool
	Files, More                                             int
	Against                                                 func() int
	Card                                                    func(func())
	Close, AskPullRequest, CompareBar, CommitBox, DrawFiles func()
}

func View(c *ui.Context, theme *ui.Theme, v ViewOptions) {
	v.Card(func() {
		ui.Row(c).Gap(8).Padding(9, 12, 9, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			ui.Icon(c, v.Icon).FontSize(15).TextColor(theme.Accent)
			ui.Text(c, "Changes").Bold()
			where := v.Root
			if where == "" {
				where = v.Dir
			}
			ui.Text(c, filepath.Base(where)).TextColor(theme.TextMuted).SingleLine().Shrink(1).Tooltip(v.LocationTooltip)
			ui.Spacer(c)
			if v.Loaded && v.Error == "" {
				ui.Text(c, v.CountLabel).FontSize(12).TextColor(theme.TextMuted).SingleLine()
			}
			if v.Loaded && v.CanPullRequest {
				pr := ui.Button(c.Key("pr"), "Pull Request").FontSize(12).Padding(2, 8).Tooltip("Open " + v.Branch + "'s pull request into " + strings.TrimPrefix(v.Base, "origin/") + ", or push it and start one, with gh")
				if v.Busy != "" {
					pr.Disabled(true)
				}
				if pr.Clicked() && v.Busy == "" {
					v.AskPullRequest()
				}
			}
			v.Close()
		})
		if v.Base != "" {
			v.CompareBar()
		}
		if v.Against() == CompareCommit && v.Files > 0 {
			v.CommitBox()
		}
		scroll := ui.Scroll(c).Grow(1)
		scroll.Children(func() {
			switch {
			case !v.Loaded:
				ui.Spinner(c).Margin(ui.Auto)
			case v.Error != "":
				ui.Text(c, v.Error).TextColor(theme.TextMuted).Margin(ui.Auto)
			case v.Files == 0 && v.Against() == CompareBase:
				ui.Text(c, "Nothing since "+v.Branch+" left "+v.Base+".").TextColor(theme.TextMuted).Margin(ui.Auto)
			case v.Files == 0:
				ui.Text(c, "No changes against HEAD.").TextColor(theme.TextMuted).Margin(ui.Auto)
			}
			v.DrawFiles()
			if v.More > 0 {
				ui.Text(c, fmt.Sprintf("and %d more", v.More)).FontSize(12).TextColor(theme.TextMuted).Padding(4)
			}
		})
	})
}
