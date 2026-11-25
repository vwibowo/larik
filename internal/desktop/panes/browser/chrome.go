package browserpane

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

type AddressBarOptions struct {
	State                                                                               *State
	RemoteLabel                                                                         string
	BackIcon, ForwardIcon, StopIcon, ReloadIcon, ExternalIcon, StarIcon, StarFilledIcon *ui.SVG
	BackShortcut, ForwardShortcut, ReloadShortcut                                       string
	GoTo                                                                                func()
	OpenExternal                                                                        func(string)
	ToggleBookmark                                                                      func()
	BookmarkAvailable, Bookmarked                                                       bool
}

func AddressBar(c *ui.Context, theme *ui.Theme, v AddressBarOptions) {
	b := v.State
	ui.Row(c.Key("address")).Gap(4).Padding(5, 10, 5, 8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
		page := func(fn func(*mygo.Page)) func() {
			return func() {
				if b.Web != nil {
					fn(b.Web.Page())
				}
			}
		}
		NavButton(c, theme, "back", v.BackIcon, "Back ("+v.BackShortcut+")", b.Back, page((*mygo.Page).GoBack))
		NavButton(c, theme, "forward", v.ForwardIcon, "Forward ("+v.ForwardShortcut+")", b.Forward, page((*mygo.Page).GoForward))
		if b.Loading {
			NavButton(c, theme, "stop", v.StopIcon, "Stop loading", true, page((*mygo.Page).Stop))
		} else {
			NavButton(c, theme, "reload", v.ReloadIcon, "Refresh Pane ("+v.ReloadShortcut+")", true, page((*mygo.Page).Reload))
		}
		field := ui.TextInput(c.Key("field"), &b.Address).Placeholder("Enter a web address").Label("Address").Grow(1).
			OnSubmit(v.GoTo).
			OnShortcut(0, ui.KeyEscape, b.CancelAddressEdit)
		if b.FocusAddress {
			field.Focus()
			b.FocusAddress = false
		}
		if v.RemoteLabel != "" {
			ui.Text(c, v.RemoteLabel).FontSize(11).TextColor(theme.TextMuted)
		}
		NavButton(c, theme, "external", v.ExternalIcon, "Open in your browser", b.URL != "" && b.URL != BlankPage, func() { v.OpenExternal(b.URL) })
		if v.Bookmarked {
			NavButton(c, theme, "bookmark", v.StarFilledIcon, "Remove bookmark", true, v.ToggleBookmark)
		} else {
			NavButton(c, theme, "bookmark", v.StarIcon, "Bookmark this page", v.BookmarkAvailable, v.ToggleBookmark)
		}
	})
}

// NavButton is a borderless icon button used by Browser chrome.
func NavButton(c *ui.Context, theme *ui.Theme, key string, icon *ui.SVG, label string, on bool, fn func()) {
	color := theme.TextMuted
	if !on {
		color = theme.TextMuted.Alpha(0.35)
	}
	b := ui.Box(c.Key(key)).Padding(4).Radius(5).Label(label).Tooltip(label).Children(func() {
		ui.Icon(c, icon).FontSize(13).TextColor(color)
	})
	if !on {
		return
	}
	if b.Hovered() {
		b.Background(theme.Text.Alpha(0.06))
	}
	if b.Clicked() {
		fn()
	}
}

type SiteBarsOptions struct {
	State                *State
	ErrorIcon, CheckIcon *ui.SVG
	Answer               func(bool)
	ShowFile             func(string)
	CloseButton          func(string, func())
}

func SiteBars(c *ui.Context, theme *ui.Theme, v SiteBarsOptions) {
	if ask := v.State.Asking; ask != nil {
		ui.Row(c.Key("asking")).Gap(8).Padding(6, 10, 6, 16).AlignItems(ui.Center).Background(theme.Accent.Alpha(0.08)).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			ui.Text(c, PageHost(ask.Origin)+" wants to use "+PermissionText(ask.Permissions)+".").FontSize(12).Grow(1).Shrink(1)
			if ui.Button(c.Key("deny"), "Don't Allow").Clicked() {
				v.Answer(false)
			}
			if ui.Button(c.Key("allow"), "Allow").Clicked() {
				v.Answer(true)
			}
		})
	}
	if d := v.State.Download; d != nil {
		ui.Row(c.Key("download")).Gap(8).Padding(6, 10, 6, 16).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			switch {
			case !d.Done:
				ui.Spinner(c).Size(12, 12)
				ui.Text(c, "Downloading "+d.Name+"…").FontSize(12).Grow(1).Shrink(1)
			case d.Err != "":
				ui.Icon(c, v.ErrorIcon).FontSize(13).TextColor(theme.Danger)
				ui.Text(c, "Couldn't download "+d.Name+": "+d.Err).FontSize(12).Grow(1).Shrink(1)
			default:
				ui.Icon(c, v.CheckIcon).FontSize(13).TextColor(theme.Success)
				ui.Text(c, "Downloaded "+d.Name).FontSize(12).Grow(1).Shrink(1)
				if v.ShowFile != nil && ui.Button(c.Key("show"), ShowInFolder()).Clicked() {
					v.ShowFile(d.Path)
				}
			}
			v.CloseButton("Dismiss", func() { v.State.Download = nil })
		})
	}
}
