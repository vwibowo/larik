package lsp

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Only the slice of the LSP protocol Larik uses.

type Position struct {
	Line      int `json:"line"`      // 0-based
	Character int `json:"character"` // 0-based, UTF-16 code units
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type locationLink struct {
	TargetURI            string `json:"targetUri"`
	TargetSelectionRange Range  `json:"targetSelectionRange"`
}

type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"` // 1 error, 2 warning, 3 info, 4 hint
	Code     any    `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

type publishDiagnostics struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type documentSymbol struct {
	Name           string           `json:"name"`
	Kind           int              `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []documentSymbol `json:"children,omitempty"`
	// SymbolInformation variant.
	Location      *Location `json:"location,omitempty"`
	ContainerName string    `json:"containerName,omitempty"`
}

var symbolKinds = []string{"", "file", "module", "namespace", "package", "class", "method", "property", "field", "constructor",
	"enum", "interface", "function", "variable", "constant", "string", "number", "boolean", "array", "object", "key",
	"null", "enum member", "struct", "event", "operator", "type parameter"}

func kindName(k int) string {
	if k > 0 && k < len(symbolKinds) {
		return symbolKinds[k]
	}
	return "symbol"
}

func pathToURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" {
		return uri
	}
	return filepath.FromSlash(u.Path)
}

// parseLocations decodes Location | Location[] | LocationLink[] | null.
func parseLocations(raw json.RawMessage) []Location {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var one Location
	if raw[0] == '{' {
		if json.Unmarshal(raw, &one) == nil && one.URI != "" {
			return []Location{one}
		}
		return nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	var out []Location
	for _, it := range items {
		var l Location
		if json.Unmarshal(it, &l) == nil && l.URI != "" {
			out = append(out, l)
			continue
		}
		var ll locationLink
		if json.Unmarshal(it, &ll) == nil && ll.TargetURI != "" {
			out = append(out, Location{URI: ll.TargetURI, Range: ll.TargetSelectionRange})
		}
	}
	return out
}

// hoverText flattens MarkupContent | MarkedString | MarkedString[].
func hoverText(raw json.RawMessage) string {
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if json.Unmarshal(raw, &h) != nil || len(h.Contents) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(h.Contents, &s) == nil {
		return s
	}
	var mc struct {
		Value string `json:"value"`
	}
	if h.Contents[0] == '{' && json.Unmarshal(h.Contents, &mc) == nil {
		return mc.Value
	}
	var list []json.RawMessage
	if json.Unmarshal(h.Contents, &list) == nil {
		var parts []string
		for _, it := range list {
			if json.Unmarshal(it, &s) == nil {
				parts = append(parts, s)
			} else if json.Unmarshal(it, &mc) == nil {
				parts = append(parts, mc.Value)
			}
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

// utf16Col converts a 0-based rune column on a line to UTF-16 units.
func utf16Col(line string, runeCol int) int {
	n := 0
	for i, r := range line {
		if utf8.RuneCountInString(line[:i]) >= runeCol {
			break
		}
		n += len(utf16.Encode([]rune{r}))
	}
	return n
}

// runeCol converts a UTF-16 column back to a 0-based rune column.
func runeCol(line string, u16 int) int {
	n, col := 0, 0
	for _, r := range line {
		if n >= u16 {
			break
		}
		n += len(utf16.Encode([]rune{r}))
		col++
	}
	return col
}
