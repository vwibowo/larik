package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testNotebook = `{
 "cells": [
  {"cell_type": "markdown", "id": "intro", "metadata": {}, "source": ["# Sales\n", "Quarterly totals."]},
  {"cell_type": "code", "execution_count": 3, "id": "load", "metadata": {"tags": ["keep"]},
   "outputs": [{"name": "stdout", "output_type": "stream", "text": ["loaded 42 rows\n"]},
               {"data": {"image/png": "AAAA", "text/plain": ["<Figure>"]}, "metadata": {}, "output_type": "display_data"}],
   "source": ["import pandas as pd\n", "df = pd.read_csv('sales.csv')"]}
 ],
 "metadata": {"kernelspec": {"language": "python", "name": "python3"}, "custom": {"x": 1}},
 "nbformat": 4,
 "nbformat_minor": 5
}`

func notebookEnv(t *testing.T) (*Env, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sales.ipynb")
	if err := os.WriteFile(path, []byte(testNotebook), 0o644); err != nil {
		t.Fatal(err)
	}
	return NewEnv(dir), path
}

func readNotebookFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var nb map[string]any
	if err := json.Unmarshal(data, &nb); err != nil {
		t.Fatalf("written notebook is not JSON: %v\n%s", err, data)
	}
	return nb
}

func TestReadNotebook(t *testing.T) {
	env, _ := notebookEnv(t)
	r := run(t, Read{}, env, `{"path":"sales.ipynb"}`)
	for _, want := range []string{"Jupyter notebook (python, 2 cells)", "[0] markdown id=intro", "# Sales\nQuarterly totals.", "[1] code id=load (run 3)", "--- output\nloaded 42 rows", "[image/png output]"} {
		if !strings.Contains(r.Content, want) {
			t.Errorf("read lacks %q:\n%s", want, r.Content)
		}
	}
	if r = run(t, Read{}, env, `{"path":"sales.ipynb","offset":1}`); strings.Contains(r.Content, "[0]") || !strings.Contains(r.Content, "[1] code") {
		t.Errorf("offset should start at that cell:\n%s", r.Content)
	}
}

func TestNotebookEdit(t *testing.T) {
	env, path := notebookEnv(t)
	run(t, Read{}, env, `{"path":"sales.ipynb"}`)

	r := run(t, NotebookEdit{}, env, `{"path":"sales.ipynb","cell_id":"load","action":"replace","source":"df = pd.read_csv('q3.csv')\ndf.head()"}`)
	if r.IsError {
		t.Fatalf("replace: %+v", r)
	}
	nb := readNotebookFile(t, path)
	cells := nb["cells"].([]any)
	load := cells[1].(map[string]any)
	if src := load["source"].([]any); len(src) != 2 || src[0] != "df = pd.read_csv('q3.csv')\n" || src[1] != "df.head()" {
		t.Errorf("source %v", src)
	}
	if len(load["outputs"].([]any)) != 0 || load["execution_count"] != nil {
		t.Errorf("stale outputs should be cleared: %v", load)
	}
	if load["metadata"].(map[string]any)["tags"] == nil || nb["metadata"].(map[string]any)["custom"] == nil {
		t.Error("cell and notebook metadata should be kept")
	}

	if r := run(t, NotebookEdit{}, env, `{"path":"sales.ipynb","cell":2,"action":"insert","cell_type":"markdown","source":"## Notes"}`); r.IsError {
		t.Fatalf("insert: %+v", r)
	}
	cells = readNotebookFile(t, path)["cells"].([]any)
	added := cells[2].(map[string]any)
	if len(cells) != 3 || added["cell_type"] != "markdown" || added["outputs"] != nil || len(added["id"].(string)) != 8 {
		t.Errorf("inserted %v", added)
	}

	if r := run(t, NotebookEdit{}, env, `{"path":"sales.ipynb","cell":0,"action":"delete"}`); r.IsError || !strings.Contains(r.Content, "2 cells now") {
		t.Fatalf("delete: %+v", r)
	}
	if r := run(t, NotebookEdit{}, env, `{"path":"sales.ipynb","cell":5,"action":"delete"}`); !r.IsError || !strings.Contains(r.Content, "out of range") {
		t.Fatalf("out of range: %+v", r)
	}
	data, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(data), "{\n \"cells\"") || !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("should be written as Jupyter writes it:\n%s", data)
	}
}

func TestNotebookEditNeedsRead(t *testing.T) {
	env, _ := notebookEnv(t)
	if r := run(t, NotebookEdit{}, env, `{"path":"sales.ipynb","cell":0,"action":"delete"}`); !r.IsError {
		t.Fatalf("editing an unread notebook should fail: %+v", r)
	}
	if r := run(t, NotebookEdit{}, env, `{"path":"notes.md","cell":0,"action":"delete"}`); !r.IsError || !strings.Contains(r.Content, "not a .ipynb") {
		t.Fatalf("got %+v", r)
	}
}
