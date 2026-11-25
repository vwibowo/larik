package databasepane

import (
	"context"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"larik/internal/database"
)

type Runtime struct {
	State        *State
	Manager      func() *database.Manager
	Profile      func(string) (database.ConnectionConfig, bool)
	Work, Update func(func())
	Alive        func() bool
	CopyText     func(string)
}

func (r Runtime) LoadSchema() {
	d := r.State
	if d == nil || d.Profile == "" || r.Manager() == nil {
		return
	}
	if p, ok := r.Profile(d.Profile); ok {
		d.Driver = p.Driver
	}
	d.Seq++
	seq, profile := d.Seq, d.Profile
	d.Loading, d.Status = true, "Loading schema…"
	r.Work(func() {
		tables, err := r.Manager().Schema(d.Ctx, profile, false)
		r.Update(func() {
			if !r.Alive() || d.Ctx.Err() != nil || d.Seq != seq {
				return
			}
			d.Loading = false
			if err != nil {
				d.Status = err.Error()
				return
			}
			oldSchema, oldTable := d.Schema, d.Table
			d.Tables, d.Schema, d.Table, d.Status = tables, "", "", ""
			d.ShowStructure = false
			if len(tables) > 0 {
				d.Schema, d.Table = tables[0].Schema, tables[0].Name
				for _, table := range tables {
					if !database.IsFunction(table.Type) {
						d.Schema, d.Table = table.Schema, table.Name
						break
					}
				}
				for _, table := range tables {
					if table.Schema == oldSchema && table.Name == oldTable {
						d.Schema, d.Table = oldSchema, oldTable
						break
					}
				}
				if database.IsFunction(FindObjectType(tables, d.Schema, d.Table)) {
					d.ShowFunctionDefinition = true
					r.LoadFunction()
				} else {
					d.ShowFunctionDefinition = false
					d.Results[DataResult].Offset = 0
					r.LoadRows(false)
				}
			}
		})
	})
}

func (r Runtime) OpenObject(object database.Table) {
	d := r.State
	d.Schema, d.Table = object.Schema, object.Name
	d.ShowStructure = false
	if database.IsFunction(object.Type) {
		d.ShowFunctionDefinition = true
		r.LoadFunction()
		return
	}
	d.ShowFunctionDefinition = false
	d.Results[DataResult].Offset = 0
	r.LoadRows(true)
}

func (r Runtime) OpenStructure(object database.Table) {
	d := r.State
	d.Schema, d.Table = object.Schema, object.Name
	d.ShowFunctionDefinition = false
	d.ShowStructure = true
}

func (r Runtime) CopyCreationStatement(object database.Table) {
	d := r.State
	if d == nil || d.Profile == "" || r.Manager() == nil || r.CopyText == nil {
		return
	}
	copyText := r.CopyText
	profile := d.Profile
	r.Work(func() {
		statement, err := r.Manager().CreationStatement(d.Ctx, profile, object.Schema, object.Name, false)
		r.Update(func() {
			if !r.Alive() || d.Ctx.Err() != nil {
				return
			}
			if err != nil {
				d.Status = err.Error()
				return
			}
			copyText(statement)
			d.Status = "Copied creation script"
		})
	})
}

func (r Runtime) CopyFunctionDefinition(object database.Table) {
	d := r.State
	if d == nil || d.Profile == "" || r.Manager() == nil || r.CopyText == nil {
		return
	}
	copyText := r.CopyText
	profile := d.Profile
	r.Work(func() {
		definition, err := r.Manager().FunctionDefinition(d.Ctx, profile, object.Schema, object.Name, false)
		r.Update(func() {
			if !r.Alive() || d.Ctx.Err() != nil {
				return
			}
			if err != nil {
				d.Status = err.Error()
				return
			}
			copyText(definition)
			d.Status = "Copied function definition"
		})
	})
}

func (r Runtime) LoadFunction() {
	d := r.State
	if d == nil || d.Profile == "" || r.Manager() == nil {
		return
	}
	d.Seq++
	seq, profile, schema, function := d.Seq, d.Profile, d.Schema, d.Table
	d.Loading, d.FunctionStatus, d.FunctionEditor = true, "Loading function definition…", nil
	r.Work(func() {
		definition, err := r.Manager().FunctionDefinition(d.Ctx, profile, schema, function, false)
		r.Update(func() {
			if !r.Alive() || d.Ctx.Err() != nil || d.Seq != seq {
				return
			}
			d.Loading = false
			if err != nil {
				d.FunctionStatus = err.Error()
				return
			}
			buffer := editor.NewBuffer(definition)
			buffer.SetLanguage(editor.LanguageNamed("sql"))
			d.FunctionEditor = &editor.State{Buffer: buffer, ReadOnly: true, NoFolding: true, NoActiveLine: true, NoScrollPastEnd: true, Label: "Function definition"}
			d.FunctionStatus = ""
		})
	})
}

func (r Runtime) LoadRows(activate bool) {
	d := r.State
	if d == nil || d.Profile == "" || d.Table == "" || r.Manager() == nil {
		return
	}
	d.Seq++
	v := &d.Results[DataResult]
	seq, profile, schema, table, offset, limit := d.Seq, d.Profile, d.Schema, d.Table, v.Offset, d.PageSize
	if limit <= 0 {
		limit = database.DefaultRows
	}
	d.Loading, d.Status, v.Status = true, "", "Loading rows…"
	if activate {
		d.SelectResult(DataResult)
	}
	r.Work(func() {
		start := time.Now()
		result, err := r.Manager().Rows(d.Ctx, profile, schema, table, offset, limit, false)
		r.Update(func() {
			if !r.Alive() || d.Ctx.Err() != nil || d.Seq != seq {
				return
			}
			d.Loading, v.Elapsed = false, time.Since(start)
			if err != nil {
				v.Status = err.Error()
				return
			}
			v.Result, v.SelectedRow, v.Status = result, -1, ""
			v.Selection.Clear()
			v.Grid.Cells = ui.ColumnRange{}
			v.Source = schema + "." + table
		})
	})
}

func (r Runtime) RunQuery() {
	d := r.State
	if d == nil || d.Profile == "" || r.Manager() == nil {
		return
	}
	query := d.SQL.Buffer.String()
	r.RunSQL(query, "SQL query", func(ctx context.Context, profile, query string) (database.Result, error) {
		return r.Manager().Query(ctx, profile, query, database.DefaultRows, false)
	})
}

func (r Runtime) RunAnalyze(query string) {
	d := r.State
	if d == nil || d.Profile == "" || r.Manager() == nil || strings.TrimSpace(query) == "" {
		return
	}
	r.RunSQL(query, "SQL analysis", func(ctx context.Context, profile, query string) (database.Result, error) {
		return r.Manager().Analyze(ctx, profile, query, database.DefaultRows, false)
	})
}

func (r Runtime) RunSQL(query, source string, run func(context.Context, string, string) (database.Result, error)) {
	d := r.State
	d.Seq++
	v := &d.Results[QueryResult]
	seq, profile := d.Seq, d.Profile
	runningStatus := "Running query…"
	if source == "SQL analysis" {
		runningStatus = "Running analysis…"
	}
	d.Loading, d.Status, v.Status = true, "", runningStatus
	d.SelectResult(QueryResult)
	r.Work(func() {
		start := time.Now()
		result, err := run(d.Ctx, profile, query)
		r.Update(func() {
			if !r.Alive() || d.Ctx.Err() != nil || d.Seq != seq {
				return
			}
			d.Loading, v.Elapsed = false, time.Since(start)
			if err != nil {
				v.Status = err.Error()
				return
			}
			v.Result, v.SelectedRow, v.Status = result, -1, ""
			v.Selection.Clear()
			v.Grid.Cells = ui.ColumnRange{}
			v.Source = source
		})
	})
}
