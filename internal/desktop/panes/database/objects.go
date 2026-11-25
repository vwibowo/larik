package databasepane

import (
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"
	"larik/internal/database"
)

func Objects(c *ui.Context, theme *ui.Theme, d *State, onOpen func(database.Table), menu func(*ui.Menu, database.Table)) {
	search := strings.ToLower(strings.TrimSpace(d.Search))
	schemaOptions := []string{"All schemas"}
	seenSchemas := make(map[string]bool)
	for _, table := range d.Tables {
		if !seenSchemas[table.Schema] {
			seenSchemas[table.Schema] = true
			name := table.Schema
			if name == "" {
				name = "Default"
			}
			schemaOptions = append(schemaOptions, name)
		}
	}
	sort.Strings(schemaOptions[1:])
	if d.SchemaFilter == "" || !ContainsString(schemaOptions, d.SchemaFilter) {
		d.SchemaFilter = "All schemas"
	}
	schemas := make([]string, 0)
	visible := make(map[string][]database.Table)
	for _, table := range d.Tables {
		schemaName := table.Schema
		if schemaName == "" {
			schemaName = "Default"
		}
		if d.SchemaFilter != "All schemas" && schemaName != d.SchemaFilter {
			continue
		}
		if search == "" || strings.Contains(strings.ToLower(table.Schema), search) || strings.Contains(strings.ToLower(table.Name), search) {
			if _, ok := visible[table.Schema]; !ok {
				schemas = append(schemas, table.Schema)
			}
			visible[table.Schema] = append(visible[table.Schema], table)
		}
	}
	ui.Column(c).Grow(1).MinWidth(0).BorderWidth(0, 1, 0, 0).BorderColor(theme.Border).Children(func() {
		ui.Row(c).Padding(7, 9).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).Children(func() {
			ui.SearchField(c.Key("db-items-search"), &d.Search).Label("Find database objects").Placeholder("Find tables, views, functions").Grow(1).MinWidth(0).
				OnShortcut(0, ui.KeyEscape, func() { d.Search = "" })
		})
		ui.Row(c).Gap(8).Padding(7, 9).BorderWidth(0, 0, 1, 0).BorderColor(theme.Border).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Schema").FontSize(11).TextColor(theme.TextMuted)
			ui.Select(c.Key("db-schema-filter"), &d.SchemaFilter, schemaOptions).Label("Filter by schema").Grow(1).MinWidth(0)
		})
		if search != "" {
			matches := 0
			for _, tables := range visible {
				matches += len(tables)
			}
			total := 0
			for _, table := range d.Tables {
				schemaName := table.Schema
				if schemaName == "" {
					schemaName = "Default"
				}
				if d.SchemaFilter == "All schemas" || d.SchemaFilter == schemaName {
					total++
				}
			}
			ui.Text(c, fmt.Sprintf("%d of %d objects", matches, total)).FontSize(11).TextColor(theme.TextMuted).Padding(7, 12)
		}
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Tree(c, func() {
				for _, schema := range schemas {
					if d.SchemaOpen == nil {
						d.SchemaOpen = map[string]*bool{}
					}
					open := d.SchemaOpen[schema]
					if open == nil {
						initial := true
						open = &initial
						d.SchemaOpen[schema] = open
					}
					if search != "" {
						*open = true
					}
					name := schema
					if name == "" {
						name = "Default"
					}
					item := ui.TreeItem(c.Key("db-schema-"+schema), name, open, func() {
						for _, table := range visible[schema] {
							label := ObjectMarker(table.Type) + "  " + table.Name
							row := ui.TreeItem(c.Key("db-table-"+table.Schema+"."+table.Name), label, nil, nil).Selected(d.Schema == table.Schema && d.Table == table.Name)
							if row.Clicked() {
								onOpen(table)
							}
							row.ContextMenu(func(m *ui.Menu) { menu(m, table) })
						}
					})
					if item.Clicked() {
						*open = true
					}
				}
			}).Padding(6, 8)
			if len(schemas) == 0 && !d.Loading {
				message := "No tables, views, or functions in this connection."
				if search != "" {
					message = "No objects match this search."
				}
				ui.Text(c, message).Padding(12).TextColor(theme.TextMuted)
			} else if len(schemas) == 0 && d.Loading {
				ui.Text(c, "Loading database objects…").Padding(12).TextColor(theme.TextMuted)
			}
		})
	})
}
