package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"larik/internal/llm"
	"larik/internal/pathpolicy"
)

// NotebookEditToolName edits Jupyter notebooks a cell at a time.
const NotebookEditToolName = "notebook_edit"

// maxCellOutput bounds each cell's outputs when a notebook is read.
const maxCellOutput = 2000

// notebook is a Jupyter notebook (nbformat 4) with every field it had
// kept as it was, so an edit changes only the cells it touches.
type notebook struct {
	fields map[string]json.RawMessage
	cells  []map[string]json.RawMessage
}

func parseNotebook(data []byte) (*notebook, error) {
	nb := &notebook{}
	if err := json.Unmarshal(data, &nb.fields); err != nil || nb.fields == nil {
		return nil, fmt.Errorf("not a Jupyter notebook: %v", err)
	}
	if err := json.Unmarshal(nb.fields["cells"], &nb.cells); err != nil {
		return nil, fmt.Errorf("not a Jupyter notebook: no cells list")
	}
	return nb, nil
}

// encode writes the notebook as Jupyter does: sorted keys, one-space
// indent, a final newline.
func (nb *notebook) encode() ([]byte, error) {
	cells, err := json.Marshal(nb.cells)
	if err != nil {
		return nil, err
	}
	nb.fields["cells"] = cells
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(nb.fields); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (nb *notebook) language() string {
	var meta struct {
		Kernel struct {
			Language string `json:"language"`
		} `json:"kernelspec"`
		Lang struct {
			Name string `json:"name"`
		} `json:"language_info"`
	}
	_ = json.Unmarshal(nb.fields["metadata"], &meta)
	if meta.Lang.Name != "" {
		return meta.Lang.Name
	}
	return meta.Kernel.Language
}

// wantsIDs reports whether cells carry ids (nbformat 4.5 and later).
func (nb *notebook) wantsIDs() bool {
	var minor int
	_ = json.Unmarshal(nb.fields["nbformat_minor"], &minor)
	return minor >= 5
}

func cellString(cell map[string]json.RawMessage, key string) string {
	var s string
	if json.Unmarshal(cell[key], &s) == nil {
		return s
	}
	var lines []string
	_ = json.Unmarshal(cell[key], &lines)
	return strings.Join(lines, "")
}

// sourceLines splits text into the list of lines, each keeping its
// newline, that Jupyter stores.
func sourceLines(text string) json.RawMessage {
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if lines == nil {
		lines = []string{}
	}
	b, _ := json.Marshal(lines)
	return b
}

// outputsText renders a cell's outputs as text, naming the ones that
// aren't, such as plots.
func outputsText(cell map[string]json.RawMessage) string {
	var outs []struct {
		Type   string                     `json:"output_type"`
		Text   json.RawMessage            `json:"text"`
		Data   map[string]json.RawMessage `json:"data"`
		EName  string                     `json:"ename"`
		EValue string                     `json:"evalue"`
	}
	_ = json.Unmarshal(cell["outputs"], &outs)
	var b strings.Builder
	for _, o := range outs {
		switch o.Type {
		case "stream":
			b.WriteString(cellString(map[string]json.RawMessage{"t": o.Text}, "t"))
		case "error":
			fmt.Fprintf(&b, "%s: %s\n", o.EName, o.EValue)
		default:
			if t, ok := o.Data["text/plain"]; ok {
				b.WriteString(cellString(map[string]json.RawMessage{"t": t}, "t"))
				b.WriteByte('\n')
			}
			for kind := range o.Data {
				if kind != "text/plain" {
					fmt.Fprintf(&b, "[%s output]\n", kind)
				}
			}
		}
	}
	return Truncate(strings.TrimRight(b.String(), "\n"), maxCellOutput)
}

// renderNotebook shows a notebook's cells from first on, with their
// index, id, type and text outputs, stopping at budget.
func renderNotebook(nb *notebook, first, budget int) string {
	var b strings.Builder
	lang := nb.language()
	if lang == "" {
		lang = "unknown language"
	}
	fmt.Fprintf(&b, "Jupyter notebook (%s, %d cells); edit cells with %s.\n", lang, len(nb.cells), NotebookEditToolName)
	for i := first; i < len(nb.cells); i++ {
		cell := nb.cells[i]
		var entry strings.Builder
		fmt.Fprintf(&entry, "\n[%d] %s", i, cellString(cell, "cell_type"))
		if id := cellString(cell, "id"); id != "" {
			fmt.Fprintf(&entry, " id=%s", id)
		}
		var count *int
		if json.Unmarshal(cell["execution_count"], &count) == nil && count != nil {
			fmt.Fprintf(&entry, " (run %d)", *count)
		}
		entry.WriteString("\n" + cellString(cell, "source") + "\n")
		if out := outputsText(cell); out != "" {
			entry.WriteString("--- output\n" + out + "\n")
		}
		if i > first && b.Len()+entry.Len() > budget {
			fmt.Fprintf(&b, "\n... (cells %d-%d not shown; read with offset=%d)\n", i, len(nb.cells)-1, i)
			break
		}
		b.WriteString(entry.String())
	}
	return b.String()
}

// NotebookEdit replaces, inserts or deletes one cell of a notebook.
type NotebookEdit struct{}

func (NotebookEdit) ReadOnly() bool { return false }
func (NotebookEdit) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        NotebookEditToolName,
		Description: "Replace, insert (before) or delete one cell of a .ipynb notebook, by index or id as read shows them. Read it first.",
		Schema: schema(`{"type":"object","properties":{
			"path":{"type":"string"},
			"cell":{"type":"integer"},
			"cell_id":{"type":"string","description":"Instead of cell; insert goes after it"},
			"action":{"type":"string","enum":["replace","insert","delete"]},
			"source":{"type":"string"},
			"cell_type":{"type":"string","enum":["code","markdown"]}},
			"required":["path","action"]}`),
	}
}

func (NotebookEdit) Run(ctx context.Context, env *Env, input json.RawMessage) Result {
	in, err := decode[struct {
		Path     string `json:"path"`
		Cell     *int   `json:"cell"`
		CellID   string `json:"cell_id"`
		Action   string `json:"action"`
		Source   string `json:"source"`
		CellType string `json:"cell_type"`
	}](input)
	if err != nil {
		return errorf("%v", err)
	}
	path := env.Abs(in.Path)
	if !strings.EqualFold(filepath.Ext(path), ".ipynb") {
		return errorf("%s is not a .ipynb notebook; use edit", path)
	}
	root, rel, err := env.writeRoot(path)
	if err != nil {
		return errorf("%v", err)
	}
	defer root.Close()
	data, err := root.ReadFile(rel)
	if err != nil {
		return errorf("%v", err)
	}
	if err := env.checkFresh(path); err != nil {
		return errorf("%v", err)
	}
	nb, err := parseNotebook(data)
	if err != nil {
		return errorf("%s: %v", path, err)
	}
	at, err := cellIndex(nb, in.Cell, in.CellID, in.Action == "insert")
	if err != nil {
		return errorf("%v", err)
	}
	var did string
	switch in.Action {
	case "replace":
		cell := nb.cells[at]
		if in.CellType != "" && in.CellType != cellString(cell, "cell_type") {
			setCellType(cell, in.CellType)
		}
		cell["source"] = sourceLines(in.Source)
		if cellString(cell, "cell_type") == "code" {
			// The outputs belonged to the old source.
			cell["outputs"], cell["execution_count"] = json.RawMessage(`[]`), json.RawMessage(`null`)
		}
		did = fmt.Sprintf("Replaced cell %d", at)
	case "insert":
		kind := in.CellType
		if kind == "" {
			kind = "code"
		}
		cell := map[string]json.RawMessage{"metadata": json.RawMessage(`{}`), "source": sourceLines(in.Source)}
		setCellType(cell, kind)
		if nb.wantsIDs() {
			id := make([]byte, 4)
			_, _ = rand.Read(id)
			cell["id"], _ = json.Marshal(hex.EncodeToString(id))
		}
		nb.cells = append(nb.cells[:at], append([]map[string]json.RawMessage{cell}, nb.cells[at:]...)...)
		did = fmt.Sprintf("Inserted a %s cell at %d", kind, at)
	case "delete":
		nb.cells = append(nb.cells[:at], nb.cells[at+1:]...)
		did = fmt.Sprintf("Deleted cell %d", at)
	default:
		return errorf("action must be replace, insert or delete")
	}
	out, err := nb.encode()
	if err != nil {
		return errorf("%v", err)
	}
	if err := env.beforeWrite(path); err != nil {
		return errorf("cannot checkpoint %s: %v", path, err)
	}
	if _, err := pathpolicy.WritePath(env.Cwd, path); err != nil {
		return errorf("%v", err)
	}
	if err := replaceFile(root, rel, out); err != nil {
		return errorf("%v", err)
	}
	env.markRead(path)
	return env.afterWrite(ctx, path, Result{Content: fmt.Sprintf("%s of %s (%d cells now)", did, path, len(nb.cells))})
}

// cellIndex finds the cell a call addresses. An insert may name the
// position just past the last cell; with an id, it goes after that cell.
func cellIndex(nb *notebook, index *int, id string, insert bool) (int, error) {
	if id != "" {
		for i, c := range nb.cells {
			if cellString(c, "id") == id {
				if insert {
					return i + 1, nil
				}
				return i, nil
			}
		}
		return 0, fmt.Errorf("no cell with id %q", id)
	}
	if index == nil {
		return 0, fmt.Errorf("give the cell's index (cell) or its id (cell_id)")
	}
	limit := len(nb.cells)
	if insert {
		limit++
	}
	if *index < 0 || *index >= limit {
		return 0, fmt.Errorf("cell %d is out of range: the notebook has %d cells", *index, len(nb.cells))
	}
	return *index, nil
}

// setCellType makes cell a code or markdown cell, with the fields that
// type has.
func setCellType(cell map[string]json.RawMessage, kind string) {
	cell["cell_type"], _ = json.Marshal(kind)
	if kind == "code" {
		cell["outputs"], cell["execution_count"] = json.RawMessage(`[]`), json.RawMessage(`null`)
		return
	}
	delete(cell, "outputs")
	delete(cell, "execution_count")
}
