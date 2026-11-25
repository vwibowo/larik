package chooserpane

import "github.com/egoist/mygo/ui"

type Option struct {
	Title, About string
	Key          ui.Key
	Icon         *ui.SVG
}

type ViewOptions struct {
	Title, Hint, DirLabel, Font string
	Options                     []Option
	ChangeDir                   func()
	Choose                      func(int)
}

// View renders the chooser. The host supplies only folder selection and the
// action to take when an option is picked.
func View(c *ui.Context, theme *ui.Theme, selection *Selection, opts ViewOptions) ui.Element {
	pick := &selection.Pick
	selection.Clamp(len(opts.Options))
	chosen, hovered := -1, -1
	var list ui.Element
	wrap := ui.Column(c).Grow(1).Center().Gap(14).Padding(24).Children(func() {
		ui.Text(c, opts.Title).Bold().FontSize(17)
		if opts.Hint != "" {
			ui.Text(c, opts.Hint).FontSize(12).TextColor(theme.TextMuted)
		}
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "in "+opts.DirLabel).FontSize(12).TextColor(theme.TextMuted).SingleLine().Shrink(1)
			if opts.ChangeDir != nil {
				change := ui.ButtonBase(c.Key("change")).Cursor(ui.CursorPointer).Label("Change the folder").Children(func() {
					ui.Text(c, "Change…").FontSize(12).TextColor(theme.Accent)
				})
				if change.Clicked() {
					opts.ChangeDir()
				}
			}
		})
		list = ui.Column(c.Key("kinds")).Gap(4).Width(380).Shrink(1).Focusable().Label("Kinds of pane").Children(func() {
			for i, option := range opts.Options {
				row := ui.ButtonBase(c.Key(option.Title)).Gap(12).Padding(10, 14).Radius(8).AlignItems(ui.Center).Cursor(ui.CursorPointer).Label(option.Title).Children(func() {
					ui.Icon(c, option.Icon).FontSize(18).TextColor(theme.Accent)
					ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
						ui.Text(c, option.Title).FontSize(13).Bold()
						ui.Text(c, option.About).FontSize(11).TextColor(theme.TextMuted)
					})
					ui.Text(c, keyName(option.Key)).FontSize(11).Font(opts.Font).TextColor(theme.TextMuted)
				})
				if i == *pick {
					row.Background(theme.Accent.Alpha(0.12))
				}
				if row.Hovered() {
					hovered = i
				}
				if row.Clicked() {
					chosen = i
				}
			}
		})
		ui.Text(c, "↑ ↓ to choose, Enter to open").FontSize(11).TextColor(theme.TextMuted)
	})
	if wrap.Clicked() && !list.FocusWithin() {
		list.Focus()
	}
	selection.SetHover(hovered)
	if list.FocusWithin() && len(opts.Options) > 0 {
		switch {
		case c.Shortcut(0, ui.KeyUp):
			selection.Move(len(opts.Options), -1)
		case c.Shortcut(0, ui.KeyDown):
			selection.Move(len(opts.Options), 1)
		case c.Shortcut(0, ui.KeyEnter):
			chosen = *pick
		}
		numberKeys := []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6}
		for i, option := range opts.Options {
			if c.Shortcut(0, option.Key) || i < len(numberKeys) && c.Shortcut(0, numberKeys[i]) {
				chosen = i
			}
		}
	}
	if chosen >= 0 && opts.Choose != nil {
		opts.Choose(chosen)
	}
	return list
}

func keyName(k ui.Key) string {
	switch k {
	case ui.KeyL:
		return "L"
	case ui.KeyW:
		return "W"
	case ui.KeyT:
		return "T"
	case ui.KeyB:
		return "B"
	case ui.KeyF:
		return "F"
	case ui.KeyD:
		return "D"
	}
	return ""
}
