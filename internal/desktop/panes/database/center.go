package databasepane

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"larik/internal/database"
)

type CenterOptions struct {
	State                       *State
	PaneOpacity                 float32
	BackIcon, CopyIcon, RunIcon *ui.SVG
	RunShortcut                 string
	IconButton                  IconButton
	OpenObject                  func(database.Table)
	RunQuery                    func()
	RunSQL                      func(string)
	RunAnalyze                  func(string)
	DrawResults                 func()
}

func Center(c *ui.Context, theme *ui.Theme, opts CenterOptions) {
	d := opts.State
	if d.ShowStructure {
		object, ok := FindObject(d.Tables, d.Schema, d.Table)
		ui.Column(c).Grow(1).MinHeight(100).Children(func() {
			ui.Row(c).Height(36).Gap(10).Padding(2, 14).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
				opts.IconButton("db-structure-back", opts.BackIcon, "Back to table rows", false, func() {
					if ok {
						opts.OpenObject(object)
					} else {
						d.ShowStructure = false
					}
				})
				ui.Text(c, "Structure").FontSize(12).Bold()
				ui.Text(c, d.Schema+"."+d.Table).FontSize(11).TextColor(theme.TextMuted).Grow(1).SingleLine()
				if ok {
					ui.Text(c, fmt.Sprintf("%d columns", len(object.Columns))).FontSize(11).TextColor(theme.TextMuted)
				}
			})
			if !ok {
				ui.Text(c, "Object structure is unavailable. Refresh the schema and try again.").Grow(1).Padding(16).TextColor(theme.TextMuted)
				return
			}
			ui.Row(c).Gap(16).Padding(8, 14).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
				ui.Text(c, "Column").FontSize(11).Bold().Grow(1)
				ui.Text(c, "Type").FontSize(11).Bold().Width(180)
				ui.Text(c, "Nullable").FontSize(11).Bold().Width(90)
				ui.Text(c, "Key").FontSize(11).Bold().Width(90)
			})
			ui.Scroll(c).Grow(1).Children(func() {
				for i, column := range object.Columns {
					ui.Row(c.Key(fmt.Sprintf("db-structure-column-%d", i))).Gap(16).Padding(8, 14).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
						ui.Text(c, column.Name).FontSize(12).Grow(1).SingleLine()
						ui.Text(c, column.Type).FontSize(11).TextColor(theme.TextMuted).Width(180).SingleLine()
						nullable := "No"
						if column.Nullable {
							nullable = "Yes"
						}
						ui.Text(c, nullable).FontSize(11).TextColor(theme.TextMuted).Width(90)
						key := "—"
						if column.PrimaryKey {
							key = "Primary"
						}
						ui.Text(c, key).FontSize(11).TextColor(theme.TextMuted).Width(90)
					})
				}
				if len(object.Columns) == 0 {
					ui.Text(c, "No columns are available for this object.").Padding(14).TextColor(theme.TextMuted)
				}
			})
		})
		return
	}
	if d.ShowFunctionDefinition {
		ui.Column(c).Grow(1).MinHeight(100).Children(func() {
			ui.Row(c).Height(36).Gap(10).Padding(2, 14).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
				opts.IconButton("db-function-back", opts.BackIcon, "Back to SQL query", false, func() {
					d.ShowFunctionDefinition = false
				})
				ui.Text(c, "ƒ").FontSize(16).TextColor(theme.Accent)
				ui.Text(c, "Function definition").FontSize(12).Bold()
				ui.Text(c, d.Schema+"."+d.Table).FontSize(11).TextColor(theme.TextMuted).Grow(1).SingleLine()
				if d.FunctionEditor != nil {
					opts.IconButton("db-copy-function", opts.CopyIcon, "Copy function definition", false, func() {
						c.Services().WriteClipboard(d.FunctionEditor.Buffer.String())
					})
				}
			})
			if d.FunctionStatus != "" {
				color := theme.Danger
				if d.Loading {
					color = theme.TextMuted
				}
				ui.Text(c, d.FunctionStatus).Grow(1).Padding(16).TextColor(color)
				return
			}
			if d.FunctionEditor == nil {
				ui.Text(c, "Function definition is unavailable.").Grow(1).Padding(16).TextColor(theme.TextMuted)
				return
			}
			codeTheme := editor.DefaultTheme(theme)
			codeTheme.Background = theme.Background
			if opts.PaneOpacity < 1 {
				codeTheme.Background = theme.Background.Alpha(0)
			}
			codeTheme.Tokens[editor.TokenKeyword].Color = theme.Accent
			codeTheme.Tokens[editor.TokenString].Color = theme.Success
			codeTheme.Tokens[editor.TokenComment].Color = theme.TextMuted
			d.FunctionEditor.Theme = codeTheme
			editor.View(c.Key("db-function-definition"), d.FunctionEditor).Grow(1).MinWidth(0).MinHeight(0)
		})
		return
	}
	ui.SplitVertical(c, &d.EditorHeight, func() {
		ui.Column(c).Grow(1).MinHeight(100).Children(func() {
			ui.Row(c).Height(36).Gap(10).Padding(2, 14).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "SQL query").FontSize(12).Bold().Grow(1)
				if !d.Narrow {
					ui.Text(c, opts.RunShortcut).FontSize(10).TextColor(theme.TextMuted).SingleLine()
				}
				run := ui.Box(c.Key("db-run")).Size(32, 32).Center().Focusable().Radius(6).Background(theme.Accent).Label("Run query (" + opts.RunShortcut + ")").Tooltip("Run query (" + opts.RunShortcut + ")").Disabled(d.Loading).Children(func() {
					ui.Icon(c, opts.RunIcon).FontSize(15).TextColor(theme.AccentText)
				})
				if run.Clicked() {
					opts.RunQuery()
				}
			})
			codeTheme := editor.DefaultTheme(theme)
			codeTheme.Background = theme.Background
			if opts.PaneOpacity < 1 {
				codeTheme.Background = theme.Background.Alpha(0)
			}
			codeTheme.Tokens[editor.TokenKeyword].Color = theme.Accent
			codeTheme.Tokens[editor.TokenString].Color = theme.Success
			codeTheme.Tokens[editor.TokenComment].Color = theme.TextMuted
			d.SQL.Theme = codeTheme
			sqlEditor := editor.View(c.Key("db-sql"), d.SQL).Grow(1).MinWidth(0).MinHeight(0)
			sqlEditor.ContextMenu(func(m *ui.Menu) {
				selected := strings.TrimSpace(d.SQL.SelectedText())
				if m.Item("Run Query").Chosen() {
					query := selected
					if query == "" {
						query = d.SQL.Buffer.String()
					}
					opts.RunSQL(query)
				}
				if m.Item("Run Analyze").Disabled(selected == "").Chosen() {
					opts.RunAnalyze(selected)
				}
				m.Separator()
				m.EditItems()
			})
		})
	}, func() { opts.DrawResults() }).Grow(1)
}
