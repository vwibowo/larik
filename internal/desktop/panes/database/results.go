package databasepane

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
	"larik/internal/database"
	"larik/internal/desktop/panes"
)

type IconButton func(id string, icon *ui.SVG, tip string, disabled bool, onClick func())

type ResultsOptions struct {
	State                                       *State
	PaneOpacity                                 float32
	HasSharedFailure                            bool
	CopyIcon, ExportIcon, BackIcon, ForwardIcon *ui.SVG
	IconButton                                  IconButton
	Export                                      func()
	LoadRows                                    func()
}

type InspectorOptions struct {
	State              *State
	BackIcon, CopyIcon *ui.SVG
	IconButton         IconButton
}

func SelectedTSV(v *ResultView, headers bool) string {
	if v.Selection.Len() == 0 {
		return ""
	}
	return TSV(v.Result, v.Grid.SelectedColumns(), v.Selection.Has, headers)
}

func Results(c *ui.Context, theme *ui.Theme, opts ResultsOptions) {
	d := opts.State
	v := d.CurrentResult()
	r := v.Result
	ui.Column(c).Grow(1).MinHeight(100).Children(func() {
		ui.Row(c).Height(36).Gap(12).Padding(4, 14).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			ui.Tabs(c.Key("db-result-tabs"), &d.ActiveResult, "Data", "Query", "Agent").Label("Database results")
			if v.Source != "" {
				ui.Text(c, v.Source).FontSize(11).TextColor(theme.TextMuted).Grow(1).SingleLine()
			}
			if len(r.Columns) > 0 {
				copyButton := ui.Box(c.Key("db-copy-menu")).Size(32, 32).Center().Focusable().Radius(6).Label("Copy selected cells").Tooltip("Copy selected cells as TSV").Disabled(v.Selection.Len() == 0).Children(func() {
					ui.Icon(c, opts.CopyIcon).FontSize(15).TextColor(theme.Text)
				})
				copyButton.PopoverMenuButton(func(m *ui.Menu) {
					for _, item := range []struct {
						label   string
						headers bool
					}{{"Copy selection", false}, {"Copy with headers", true}} {
						if m.Item(item.label).Chosen() {
							if selected := SelectedTSV(v, item.headers); selected != "" {
								c.Services().WriteClipboard(selected)
							}
						}
					}
				})
				exportTip := "Export loaded rows as CSV"
				if r.Truncated || r.ValuesTruncated {
					exportTip += " (partial result or shortened values)"
				}
				opts.IconButton("db-export", opts.ExportIcon, exportTip, false, func() { opts.Export() })
			}
		})
		if len(r.Columns) == 0 {
			message := "Select a table to browse its rows."
			switch d.ActiveResult {
			case QueryResult:
				message = "Run a read-only SQL query to see its results."
			case AgentResult:
				message = "Agent results appear here when access is enabled."
			}
			ui.Text(c, message).Grow(1).Padding(16).TextColor(theme.TextMuted)
		} else {
			cols := make([]ui.TableColumn, len(r.Columns)+1)
			cols[0] = ui.TableColumn{ID: "_row", Title: "#", Width: 46, Fixed: true, RowHeader: true}
			for i, name := range r.Columns {
				align := ui.Start
				for _, row := range r.Rows {
					if i >= len(row) || row[i] == nil {
						continue
					}
					switch row[i].(type) {
					case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
						align = ui.End
					}
					break
				}
				cols[i+1] = ui.TableColumn{ID: fmt.Sprintf("%d", i), Title: name, Width: 170, MinWidth: 90, Align: align}
			}
			v.Grid.Selected = &v.SelectedRow
			v.Grid.Selection = &v.Selection
			v.Grid.CellSelect = true
			v.Grid.Label = func(row int) string { return fmt.Sprintf("Row %d", v.Offset+row+1) }
			v.Grid.RowBackground = func(row int) ui.Color {
				if row%2 == 1 {
					return theme.Text.Alpha(0.035)
				}
				return ui.Transparent
			}
			gridTheme := *theme
			gridTheme.Spacing = 3
			gridTheme.SurfaceHover = theme.Text.Alpha(0.08)
			if opts.PaneOpacity < 1 {
				gridTheme.Background = theme.Background.Alpha(0)
				gridTheme.InverseText = theme.Background
			}
			ui.Themed(c, &gridTheme).Grow(1).MinWidth(0).Children(func() {
				grid := ui.Table(c.Key(fmt.Sprintf("db-grid-%d", d.ActiveResult)), &v.Grid, cols, len(r.Rows), func(row, col int) {
					if col == 0 {
						ui.Text(c, fmt.Sprint(v.Offset+row+1)).FontSize(10).TextColor(theme.TextMuted).SingleLine()
						return
					}
					col--
					value := "NULL"
					muted := true
					if col < len(r.Rows[row]) && r.Rows[row][col] != nil {
						value, muted = fmt.Sprint(r.Rows[row][col]), false
					}
					cell := ui.Text(c, value).FontSize(11).SingleLine().Tooltip(value)
					if v.Grid.CellSelected(row, col+1) {
						cell.TextColor(theme.AccentText)
					} else if muted {
						cell.TextColor(theme.TextMuted)
					}
				}).Grow(1)
				grid.OnShortcut(ui.Cmd, ui.KeyC, func() {
					if selected := SelectedTSV(v, false); selected != "" {
						c.Services().WriteClipboard(selected)
					}
				})
				if v.FocusGrid && v.Grid.Focus(c) {
					v.FocusGrid = false
				}
				if grid.Submitted() && d.HasSelectedRow() {
					v.FocusGrid = !d.Compact
					d.ShowDetails = true
					if d.Compact {
						d.Region = panes.DatabaseDetailsRegion
					}
				}
			})
		}
		ui.Row(c).Height(32).Gap(8).Padding(4, 14).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(theme.Border).Children(func() {
			hasSharedFailure := opts.HasSharedFailure
			if v.Status != "" || d.Status != "" {
				status := v.Status
				if d.Status != "" {
					status = d.Status
				}
				if !hasSharedFailure {
					color := theme.Danger
					if d.Loading || strings.HasPrefix(status, "Exported ") {
						color = theme.TextMuted
					}
					ui.Text(c, status).FontSize(11).TextColor(color).Grow(1).SingleLine()
				}
			} else {
				label := fmt.Sprintf("%d rows · %s", len(r.Rows), Timing(v.Elapsed))
				if d.ActiveResult == DataResult && len(r.Rows) > 0 {
					label = fmt.Sprintf("Rows %d–%d · %s", v.Offset+1, v.Offset+len(r.Rows), Timing(v.Elapsed))
				}
				if r.Truncated {
					if d.ActiveResult == DataResult && r.NextOffset > 0 {
						label += " · More rows available"
					} else {
						label += " · Truncated at row or 1 MiB limit"
					}
				}
				if r.ValuesTruncated {
					label += " · Some values shortened"
				}
				ui.Text(c, label).FontSize(11).TextColor(theme.TextMuted).Grow(1).SingleLine()
			}
			if d.ActiveResult == DataResult {
				if d.PageSize <= 0 {
					d.PageSize = database.DefaultRows
				}
				pageSize := fmt.Sprint(d.PageSize)
				if ui.Select(c.Key("db-page-size"), &pageSize, []string{"25", "50", "100", "250", "500"}).Width(72).Label("Rows per page").Changed() {
					fmt.Sscan(pageSize, &d.PageSize)
					v.Offset = 0
					opts.LoadRows()
				}
				prev := ui.Box(c.Key("db-prev")).Size(32, 32).Center().Focusable().Radius(6).Label("Previous page").Tooltip("Previous page").Disabled(d.Loading || v.Offset == 0).Children(func() {
					ui.Icon(c, opts.BackIcon).FontSize(15).TextColor(theme.Text)
				})
				if prev.Clicked() {
					v.Offset = max(0, v.Offset-d.PageSize)
					opts.LoadRows()
				}
				next := ui.Box(c.Key("db-next")).Size(32, 32).Center().Focusable().Radius(6).Label("Next page").Tooltip("Next page").Disabled(d.Loading || r.NextOffset == 0).Children(func() {
					ui.Icon(c, opts.ForwardIcon).FontSize(15).TextColor(theme.Text)
				})
				if next.Clicked() {
					v.Offset = r.NextOffset
					opts.LoadRows()
				}
			}
		})
	})
}

func Inspector(c *ui.Context, theme *ui.Theme, opts InspectorOptions) {
	d := opts.State
	v := d.CurrentResult()
	ui.Column(c).Grow(1).MinWidth(0).BorderWidth(0, 0, 0, 1).BorderColor(theme.Border).Children(func() {
		ui.Row(c).Height(38).Padding(8, 14).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Details").FontSize(12).Bold().Grow(1)
			if d.Compact {
				opts.IconButton("db-details-back", opts.BackIcon, "Back to results", false, func() { d.Region = panes.DatabaseResultsRegion })
			}
		})
		if !d.HasSelectedRow() {
			ui.Text(c, "Select a row to inspect its fields.").Padding(12).TextColor(theme.TextMuted)
			return
		}
		row := v.Result.Rows[v.SelectedRow]
		ui.Scroll(c).Grow(1).Children(func() {
			for i, name := range v.Result.Columns {
				value := "NULL"
				if i < len(row) && row[i] != nil {
					value = fmt.Sprint(row[i])
				}
				ui.Column(c.Key(fmt.Sprintf("db-field-%d", i))).Padding(8, 12).Gap(3).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Children(func() {
						ui.Text(c, name).FontSize(10).TextColor(theme.TextMuted).Grow(1)
						opts.IconButton(fmt.Sprintf("db-copy-field-%d", i), opts.CopyIcon, "Copy "+name, false, func() { c.Services().WriteClipboard(value) })
					})
					ui.Text(c, value).FontSize(11).MaxLines(8).Tooltip(value).Selectable()
				})
			}
		})
	})
}
