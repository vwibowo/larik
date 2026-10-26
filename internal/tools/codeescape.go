package tools

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/dop251/goja/ast"
	"github.com/dop251/goja/parser"
)

// A backslash before a character with no escape meaning, such as \| or
// \d, is dropped from a JavaScript string. Models write them meaning the
// backslash for grep, sed or a regex, and the command then runs without
// it and quietly matches nothing: a check that "finds no problems".

// stringEscapes and templateEscapes are the characters after a backslash
// that mean something in a string and in a template literal.
const (
	stringEscapes   = "'\"\\bfnrtvxu0123456789\r\n\u2028\u2029"
	templateEscapes = stringEscapes + "`$"
)

var astPkg = reflect.TypeOf(ast.StringLiteral{}).PkgPath()

// lostEscapes lists, at most four, the escapes in code's strings and
// template literals that JavaScript drops the backslash of. Regular
// expression literals keep theirs and aren't looked at.
func lostEscapes(code string) []string {
	prog, err := parser.ParseFile(nil, "", code, 0)
	if err != nil {
		if prog, err = parser.ParseFile(nil, "", "(async () => {\n"+code+"\n})()", 0); err != nil {
			return nil
		}
	}
	var found []string
	seen := map[string]bool{}
	collect := func(raw, valid string) {
		for i := 0; i < len(raw)-1 && len(found) < 4; i++ {
			if raw[i] != '\\' {
				continue
			}
			r := []rune(raw[i+1:])[0]
			if !strings.ContainsRune(valid, r) && !seen[string(r)] {
				seen[string(r)] = true
				found = append(found, `\`+string(r))
			}
			i++ // the escaped character, even a backslash
		}
	}
	walkAST(reflect.ValueOf(prog), func(n any) {
		switch n := n.(type) {
		case *ast.StringLiteral:
			collect(n.Literal, stringEscapes)
		case *ast.TemplateElement:
			collect(n.Literal, templateEscapes)
		}
	})
	return found
}

// walkAST calls visit with every node under v, staying in goja's ast.
func walkAST(v reflect.Value, visit func(any)) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() || v.Elem().Kind() != reflect.Struct || v.Elem().Type().PkgPath() != astPkg {
			return
		}
		if v.CanInterface() {
			visit(v.Interface())
		}
		walkAST(v.Elem(), visit)
	case reflect.Interface:
		if !v.IsNil() {
			walkAST(v.Elem(), visit)
		}
	case reflect.Struct:
		for i := range v.NumField() {
			walkAST(v.Field(i), visit)
		}
	case reflect.Slice:
		for i := range v.Len() {
			walkAST(v.Index(i), visit)
		}
	}
}

// escapeNote tells the model which backslashes its script lost.
func escapeNote(lost []string) string {
	kept := strings.ReplaceAll(strings.Join(lost, " "), `\`, `\\`)
	return fmt.Sprintf("[note: JavaScript strings drop the backslash in %s, so the tools got the text without it. "+
		"If a command, pattern or file needed it, double it (%s).]", strings.Join(lost, " "), kept)
}
