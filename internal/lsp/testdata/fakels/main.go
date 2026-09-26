// Command fakels is a tiny language server for tests. Files use the .fake
// extension. Every line containing BAD gets an error; a file containing
// BREAKOTHER also produces an error in other.fake. "def NAME" lines are
// definitions for definition/references/symbols.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

type msg struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  any              `json:"result,omitempty"`
}

var (
	out   = bufio.NewWriter(os.Stdout)
	outMu sync.Mutex
	docs  = map[string]string{}
	root  string
)

func send(m msg) {
	m.JSONRPC = "2.0"
	b, _ := json.Marshal(m)
	outMu.Lock()
	fmt.Fprintf(out, "Content-Length: %d\r\n\r\n%s", len(b), b)
	out.Flush()
	outMu.Unlock()
}

func read(r *bufio.Reader) (msg, error) {
	n := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return msg{}, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if v, ok := strings.CutPrefix(line, "Content-Length: "); ok {
			n, _ = strconv.Atoi(v)
		}
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return msg{}, err
	}
	var m msg
	return m, json.Unmarshal(body, &m)
}

func pos(line, char int) map[string]int { return map[string]int{"line": line, "character": char} }

func rng(line, char, n int) map[string]any {
	return map[string]any{"start": pos(line, char), "end": pos(line, char+n)}
}

func publish(uri string) {
	var diags []any
	for i, l := range strings.Split(docs[uri], "\n") {
		if c := strings.Index(l, "BAD"); c >= 0 {
			diags = append(diags, map[string]any{"range": rng(i, c, 3), "severity": 1, "source": "fakels", "message": "found BAD"})
		}
		if c := strings.Index(l, "MEH"); c >= 0 {
			diags = append(diags, map[string]any{"range": rng(i, c, 3), "severity": 2, "message": "meh"})
		}
	}
	if diags == nil {
		diags = []any{}
	}
	send(msg{Method: "textDocument/publishDiagnostics", Params: mustJSON(map[string]any{"uri": uri, "diagnostics": diags})})
	if strings.Contains(docs[uri], "BREAKOTHER") {
		other := "file://" + root + "/other.fake"
		send(msg{Method: "textDocument/publishDiagnostics", Params: mustJSON(map[string]any{"uri": other, "diagnostics": []any{
			map[string]any{"range": rng(0, 0, 1), "severity": 1, "message": "broken by change"}}})})
	}
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

// defs finds "def NAME" across open docs.
func defs(name string) []any {
	var locs []any
	for uri, text := range docs {
		for i, l := range strings.Split(text, "\n") {
			if c := strings.Index(l, "def "+name); c >= 0 {
				locs = append(locs, map[string]any{"uri": uri, "range": rng(i, c+4, len(name))})
			}
		}
	}
	return locs
}

func wordAt(uri string, line, char int) string {
	lines := strings.Split(docs[uri], "\n")
	if line >= len(lines) {
		return ""
	}
	l := []rune(lines[line])
	s, e := char, char
	for s > 0 && isWord(l[s-1]) {
		s--
	}
	for e < len(l) && isWord(l[e]) {
		e++
	}
	return string(l[s:e])
}

func isWord(r rune) bool {
	return r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
}

func main() {
	r := bufio.NewReader(os.Stdin)
	for {
		m, err := read(r)
		if err != nil {
			return
		}
		var p struct {
			RootURI      string `json:"rootUri"`
			TextDocument struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"textDocument"`
			ContentChanges []struct{ Text string } `json:"contentChanges"`
			Position       struct{ Line, Character int }
			Query          string `json:"query"`
		}
		_ = json.Unmarshal(m.Params, &p)
		switch m.Method {
		case "initialize":
			root = strings.TrimPrefix(p.RootURI, "file://")
			send(msg{ID: m.ID, Result: map[string]any{"capabilities": map[string]any{"textDocumentSync": 1}}})
		case "initialized":
			id := json.RawMessage(`"cfg-1"`)
			send(msg{ID: &id, Method: "workspace/configuration", Params: mustJSON(map[string]any{"items": []any{map[string]any{"section": "fake"}}})})
		case "textDocument/didOpen":
			docs[p.TextDocument.URI] = p.TextDocument.Text
			publish(p.TextDocument.URI)
		case "textDocument/didChange":
			docs[p.TextDocument.URI] = p.ContentChanges[len(p.ContentChanges)-1].Text
			publish(p.TextDocument.URI)
		case "textDocument/definition":
			send(msg{ID: m.ID, Result: defs(wordAt(p.TextDocument.URI, p.Position.Line, p.Position.Character))})
		case "textDocument/references":
			name := wordAt(p.TextDocument.URI, p.Position.Line, p.Position.Character)
			var locs []any
			for uri, text := range docs {
				for i, l := range strings.Split(text, "\n") {
					if c := strings.Index(l, name); c >= 0 {
						locs = append(locs, map[string]any{"uri": uri, "range": rng(i, c, len(name))})
					}
				}
			}
			send(msg{ID: m.ID, Result: locs})
		case "textDocument/hover":
			send(msg{ID: m.ID, Result: map[string]any{"contents": map[string]any{"kind": "markdown", "value": "hover: " + wordAt(p.TextDocument.URI, p.Position.Line, p.Position.Character)}}})
		case "textDocument/documentSymbol":
			var syms []any
			for i, l := range strings.Split(docs[p.TextDocument.URI], "\n") {
				if c := strings.Index(l, "def "); c >= 0 {
					name := strings.Fields(l[c+4:])[0]
					syms = append(syms, map[string]any{"name": name, "kind": 12, "range": rng(i, 0, len(l)), "selectionRange": rng(i, c+4, len(name))})
				}
			}
			send(msg{ID: m.ID, Result: syms})
		case "workspace/symbol":
			var out []any
			for _, l := range defs(p.Query) {
				out = append(out, map[string]any{"name": p.Query, "kind": 12, "location": l})
			}
			send(msg{ID: m.ID, Result: out})
		case "shutdown":
			send(msg{ID: m.ID, Result: nil})
		case "exit":
			return
		default:
			if m.ID != nil && m.Method != "" {
				send(msg{ID: m.ID, Result: nil})
			}
		}
	}
}
