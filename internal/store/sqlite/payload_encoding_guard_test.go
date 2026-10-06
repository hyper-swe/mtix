// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// MTIX-107.25: prevent accidental encoder-error discards in code and fixtures.
// This syntax guard follows direct calls and simple local or package aliases,
// including ordinary parentheses, import quoting and multiple-RHS helper calls.
// Deliberately hidden aliases via assertions/type switches, reassigned closures,
// loops/conditionals or reflection require control-flow/type analysis and are
// outside this guard. The known-limit test characterizes that boundary.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func payloadEncoderAliases(file *ast.File) map[string]bool {
	aliases := map[string]bool{}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != "github.com/hyper-swe/mtix/internal/model" {
			continue
		}
		name := "model"
		if imp.Name != nil {
			name = imp.Name.Name
		}
		aliases[name] = true
	}
	return aliases
}

// The checked emitter returns one error; encoders return payload then error.
func payloadErrorIndex(expr ast.Expr, imports map[string]bool, aliases map[string]int) int {
	switch e := payloadUnparenthesize(expr).(type) {
	case *ast.Ident:
		if index, ok := aliases[e.Name]; ok {
			return index
		}
		if imports["."] && e.Name == "EncodePayload" {
			return 1
		}
	case *ast.SelectorExpr:
		if e.Sel.Name == "emitPayload" {
			return 0
		}
		if e.Sel.Name == "encodePayload" || e.Sel.Name == "encodePayloadFn" {
			return 1
		}
		owner, ok := e.X.(*ast.Ident)
		if ok && imports[owner.Name] && e.Sel.Name == "EncodePayload" {
			return 1
		}
	}
	return -1
}

func payloadLocalAliases(node ast.Node, imports map[string]bool, initial map[string]int) map[string]int {
	aliases := map[string]int{}
	for name, value := range initial {
		aliases[name] = value
	}
	if fn, ok := node.(*ast.FuncDecl); ok {
		payloadRemoveLocalShadows(fn, imports, aliases)
	}
	// Repeat to follow ordinary local chains such as marshal := model.EncodePayload;
	// encode := marshal. The scanner is a syntax guard, not control-flow analysis.
	for changed := true; changed; {
		changed = false
		ast.Inspect(node, func(n ast.Node) bool {
			var names []ast.Expr
			var values []ast.Expr
			switch a := n.(type) {
			case *ast.AssignStmt:
				names, values = a.Lhs, a.Rhs
			case *ast.ValueSpec:
				for _, id := range a.Names {
					names = append(names, id)
				}
				values = a.Values
			}
			if len(names) != len(values) {
				return true
			}
			for i, value := range values {
				if id, ok := names[i].(*ast.Ident); ok {
					_, known := aliases[id.Name]
					index := payloadErrorIndex(value, imports, aliases)
					if !known && index >= 0 {
						aliases[id.Name] = index
						changed = true
					}
				}
			}
			return true
		})
	}
	return aliases
}

func discardedPayloadErrors(source string) ([]token.Position, error) {
	return discardedPayloadErrorsWithGlobals(source, nil)
}

func discardedPayloadErrorsWithGlobals(source string, initial map[string]int) ([]token.Position, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "source.go", source, 0)
	if err != nil {
		return nil, err
	}
	imports := payloadEncoderAliases(file)
	globals := payloadFileGlobalAliases(file, initial)
	var hits []token.Position
	for _, decl := range file.Decls {
		aliases := globals
		if fn, ok := decl.(*ast.FuncDecl); ok {
			aliases = payloadLocalAliases(fn, imports, globals)
		}
		hits = append(hits, payloadDiscardPositions(decl, fset, imports, aliases)...)
	}
	return hits, nil
}

func payloadFileGlobalAliases(file *ast.File, initial map[string]int) map[string]int {
	globals := initial
	if globals == nil {
		globals = map[string]int{}
	}
	for _, decl := range file.Decls {
		if _, ok := decl.(*ast.GenDecl); ok {
			globals = payloadLocalAliases(decl, payloadEncoderAliases(file), globals)
		}
	}
	return globals
}

func payloadRemoveLocalShadows(fn *ast.FuncDecl, imports map[string]bool, aliases map[string]int) {
	ast.Inspect(fn, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.AssignStmt:
			if d.Tok == token.DEFINE {
				payloadRemoveShadowNames(d.Lhs, d.Rhs, imports, aliases)
			}
		case *ast.ValueSpec:
			names := make([]ast.Expr, len(d.Names))
			for i, name := range d.Names {
				names[i] = name
			}
			payloadRemoveShadowNames(names, d.Values, imports, aliases)
		case *ast.Field:
			for _, name := range d.Names {
				delete(aliases, name.Name)
			}
		}
		return true
	})
}

func payloadRemoveShadowNames(names, values []ast.Expr, imports map[string]bool, aliases map[string]int) {
	for i, name := range names {
		id, ok := name.(*ast.Ident)
		if !ok {
			continue
		}
		if len(names) == len(values) && payloadErrorIndex(values[i], imports, aliases) >= 0 {
			continue
		}
		delete(aliases, id.Name)
	}
}

func payloadPackageGlobalAliases(sources map[string]string) (map[string]map[string]int, error) {
	files := map[string]*ast.File{}
	packages := map[string]string{}
	for path, source := range sources {
		file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil {
			return nil, err
		}
		files[path] = file
		packages[path] = filepath.Dir(path) + "/" + file.Name.Name
	}
	globals := map[string]map[string]int{}
	for changed := true; changed; {
		changed = false
		for path, file := range files {
			key := packages[path]
			before := len(globals[key])
			globals[key] = payloadFileGlobalAliases(file, globals[key])
			if len(globals[key]) > before {
				changed = true
			}
		}
	}
	result := map[string]map[string]int{}
	for path := range files {
		result[path] = globals[packages[path]]
	}
	return result, nil
}

func payloadGuardSources(root string) (map[string]string, error) {
	sources := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err == nil {
			sources[path] = string(data)
		}
		return err
	})
	return sources, err
}

func payloadDiscardPositions(node ast.Node, fset *token.FileSet, imports map[string]bool, aliases map[string]int) []token.Position {
	var hits []token.Position
	ast.Inspect(node, func(n ast.Node) bool {
		for _, call := range payloadDiscardCalls(n, imports, aliases) {
			if call != nil && payloadErrorIndex(call.Fun, imports, aliases) >= 0 {
				hits = append(hits, fset.Position(call.Pos()))
			}
		}
		return true
	})
	return hits
}

// Parentheses preserve the call and result count at both the callee and RHS.
func payloadUnparenthesize(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

func payloadDiscardCalls(n ast.Node, imports map[string]bool, aliases map[string]int) []*ast.CallExpr {
	switch a := n.(type) {
	case *ast.AssignStmt:
		return payloadBlankCalls(a.Rhs, payloadBlankIdentifiers(a.Lhs), imports, aliases)
	case *ast.ValueSpec:
		names := make([]ast.Expr, len(a.Names))
		for i, name := range a.Names {
			names[i] = name
		}
		return payloadBlankCalls(a.Values, payloadBlankIdentifiers(names), imports, aliases)
	case *ast.ExprStmt:
		call, _ := payloadUnparenthesize(a.X).(*ast.CallExpr)
		return []*ast.CallExpr{call}
	case *ast.GoStmt:
		return []*ast.CallExpr{a.Call}
	case *ast.DeferStmt:
		return []*ast.CallExpr{a.Call}
	}
	return nil
}

func payloadBlankIdentifiers(names []ast.Expr) []bool {
	blanks := make([]bool, len(names))
	for i, name := range names {
		id, ok := name.(*ast.Ident)
		blanks[i] = ok && id.Name == "_"
	}
	return blanks
}

func payloadBlankCalls(values []ast.Expr, blanks []bool, imports map[string]bool, aliases map[string]int) []*ast.CallExpr {
	var calls []*ast.CallExpr
	for i, value := range values {
		call, ok := payloadUnparenthesize(value).(*ast.CallExpr)
		if !ok {
			continue
		}
		index := payloadErrorIndex(call.Fun, imports, aliases)
		if len(values) == 1 {
			// A sole multi-value encoder maps its error to result index one.
			if index >= 0 && len(blanks) == index+1 && blanks[index] {
				calls = append(calls, call)
			}
		} else if index == 0 && len(values) == len(blanks) && blanks[i] {
			// Multiple RHS expressions each yield one result in legal Go assignments.
			calls = append(calls, call)
		}
	}
	return calls
}

func TestPayloadEncodingGuard_DiscardVariants(t *testing.T) {
	cases := []struct {
		name, imp, body string
		bad             bool
	}{
		{"blank tuple", "model \"github.com/hyper-swe/mtix/internal/model\"", "raw, _ := model.EncodePayload(nil); _ = raw", true},
		{"assignment", "model \"github.com/hyper-swe/mtix/internal/model\"", "var raw []byte; raw, _ = model.EncodePayload(nil); _ = raw", true},
		{"var tuple", "model \"github.com/hyper-swe/mtix/internal/model\"", "var raw, _ = model.EncodePayload(nil); _ = raw", true},
		{"renamed import", "domain \"github.com/hyper-swe/mtix/internal/model\"", "raw, _ := domain.EncodePayload(nil); _ = raw", true},
		{"raw import", "model `github.com/hyper-swe/mtix/internal/model`", "raw, _ := model.EncodePayload(nil); _ = raw", true},
		{"dot import", ". \"github.com/hyper-swe/mtix/internal/model\"", "raw, _ := EncodePayload(nil); _ = raw", true},
		{"function alias", "model \"github.com/hyper-swe/mtix/internal/model\"", "encode := model.EncodePayload; raw, _ := encode(nil); _ = raw", true},
		{"alias chain", "model \"github.com/hyper-swe/mtix/internal/model\"", "marshal := model.EncodePayload; encode := marshal; raw, _ := (encode)(nil); _ = raw", true},
		{"standalone", "model \"github.com/hyper-swe/mtix/internal/model\"", "model.EncodePayload(nil)", true},
		{"defer", "model \"github.com/hyper-swe/mtix/internal/model\"", "defer model.EncodePayload(nil)", true},
		{"goroutine", "model \"github.com/hyper-swe/mtix/internal/model\"", "go model.EncodePayload(nil)", true},
		{"discarded helper", "", "var s *Store; _ = s.emitPayload(nil,nil,emitParams{},nil)", true},
		{"private encoder", "", "var s *Store; raw, _ := s.encodePayload(nil); _ = raw", true},
		{"checked", "model \"github.com/hyper-swe/mtix/internal/model\"", "raw, err := model.EncodePayload(nil); if err != nil { return err }; _ = raw", false},
		{"alias checked", "model \"github.com/hyper-swe/mtix/internal/model\"", "encode := model.EncodePayload; raw, err := encode(nil); if err != nil { return err }; _ = raw", false},
		{"other package", "other \"example.com/other\"", "raw, _ := other.EncodePayload(nil); _ = raw", false},
		{"comment", "", "// raw, _ := model.EncodePayload(nil)\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := "package sqlite\n"
			if tc.imp != "" {
				source += "import " + tc.imp + "\n"
			}
			source += "func probe() error {" + tc.body + "; return nil }"
			hits, err := discardedPayloadErrors(source)
			require.NoError(t, err)
			require.Equal(t, tc.bad, len(hits) > 0, hits)
		})
	}
}

func TestPayloadEncodingGuard_InternalCallsCheckErrors(t *testing.T) {
	sources, err := payloadGuardSources("../..")
	require.NoError(t, err)
	globals, err := payloadPackageGlobalAliases(sources)
	require.NoError(t, err)
	for path, source := range sources {
		hits, err := discardedPayloadErrorsWithGlobals(source, globals[path])
		require.NoError(t, err)
		for _, hit := range hits {
			t.Errorf("%s:%d: discarded payload encoder error", path, hit.Line)
		}
	}
}

func TestPayloadEncodingGuard_AliasesDoNotLeakBetweenFunctions(t *testing.T) {
	source := `package probe
 import model "github.com/hyper-swe/mtix/internal/model"
 func checked() error { encode:=model.EncodePayload; _,err:=encode(nil); return err }
 func unrelated() { encode:=func(any)([]byte,error){return nil,nil}; _,_=encode(nil) }
 `
	hits, err := discardedPayloadErrors(source)
	require.NoError(t, err)
	require.Empty(t, hits)
}

func TestPayloadEncodingGuard_ParenthesizedAndSingleResult(t *testing.T) {
	cases := []struct {
		name, body string
		bad        bool
	}{
		{"tuple declaration", "raw, _ := (model.EncodePayload(nil)); _ = raw", true},
		{"tuple reassignment", "var raw []byte; raw, _ = ((model.EncodePayload(nil))); _ = raw", true},
		{"var tuple", "var raw, _ = (((model.EncodePayload)(nil))); _ = raw", true},
		{"standalone", "((model.EncodePayload(nil)))", true},
		{"single var helper", "var s *Store; var _ = s.emitPayload(nil,nil,emitParams{},nil)", true},
		{"parenthesized single var", "var s *Store; var _ = (((s.emitPayload)(nil,nil,emitParams{},nil)))", true},
		{"method alias", "var s *Store; emit := ((s.emitPayload)); var _ = ((emit(nil,nil,emitParams{},nil)))", true},
		{"encoder alias", "encode := ((model.EncodePayload)); var raw, _ = ((encode(nil))); _ = raw", true},
		{"helper assignment", "var s *Store; _ = ((s.emitPayload(nil,nil,emitParams{},nil)))", true},
		{"parenthesized defer", "defer ((model.EncodePayload))(nil)", true},
		{"parenthesized go", "var s *Store; go ((s.emitPayload))(nil,nil,emitParams{},nil)", true},
		{"checked tuple", "raw, err := ((model.EncodePayload(nil))); if err != nil { return err }; _ = raw", false},
		{"checked var tuple", "var raw, err = ((model.EncodePayload(nil))); if err != nil { return err }; _ = raw", false},
		{"checked var helper", "var s *Store; var err = ((s.emitPayload(nil,nil,emitParams{},nil))); if err != nil { return err }", false},
		{"checked method alias", "var s *Store; emit := ((s.emitPayload)); var err = ((emit(nil,nil,emitParams{},nil))); if err != nil { return err }", false},
		{"unrelated encode", "encode := func(any)([]byte,error){return nil,nil}; var raw, _ = ((encode(nil))); _ = raw", false},
		{"unrelated one result", "encode := func()error{return nil}; var _ = ((encode()))", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := "package sqlite\nimport model \"github.com/hyper-swe/mtix/internal/model\"\nfunc probe() error {" + tc.body + "; return nil }"
			hits, err := discardedPayloadErrors(source)
			require.NoError(t, err)
			require.Equal(t, tc.bad, len(hits) > 0, hits)
		})
	}
}

func TestPayloadEncodingGuard_MultipleRHS(t *testing.T) {
	for _, declaration := range []string{":=", "=", "var"} {
		for _, position := range []int{0, 2, 4} {
			for _, alias := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/slot%d/alias%t", declaration, position, alias), func(t *testing.T) {
					body := payloadMultipleRHSBody(declaration, position, alias, true, false)
					hits, err := discardedPayloadErrors(payloadGuardProbe(body))
					require.NoError(t, err)
					require.Len(t, hits, 1)
				})
			}
			for _, unrelated := range []bool{false, true} {
				t.Run(fmt.Sprintf("checked/%s/slot%d/unrelated%t", declaration, position, unrelated), func(t *testing.T) {
					body := payloadMultipleRHSBody(declaration, position, true, false, unrelated)
					hits, err := discardedPayloadErrors(payloadGuardProbe(body))
					require.NoError(t, err)
					require.Empty(t, hits)
				})
			}
		}
	}
}

func payloadGuardProbe(body string) string {
	return "package sqlite\nimport model \"github.com/hyper-swe/mtix/internal/model\"\nfunc probe() error {" + body + ";return nil}"
}

func payloadMultipleRHSBody(declaration string, position int, alias, discard, unrelated bool) string {
	prefix := "var s *Store; "
	call := "((s.emitPayload)(nil,nil,emitParams{},nil))"
	if alias {
		prefix += "emit := ((s.emitPayload)); forward := (emit); "
		call = "((forward)(nil,nil,emitParams{},nil))"
	}
	if unrelated {
		prefix = "encode := func()error{return nil}; "
		call = "((encode()))"
	}
	names, values := []string{"n0", "n1", "n2", "n3", "n4"}, []string{"0", "1", "2", "3", "4"}
	names[position], values[position] = "err", call
	if discard || unrelated {
		names[position] = "_"
	}
	if declaration == "=" {
		for i, name := range names {
			if name == "_" {
				continue
			}
			kind := "int"
			if i == position {
				kind = "error"
			}
			prefix += "var " + name + " " + kind + "; "
		}
	}
	assignment := strings.Join(names, ",") + " " + declaration + " " + strings.Join(values, ",") + "; "
	if declaration == "var" {
		assignment = "var " + strings.Join(names, ",") + " = " + strings.Join(values, ",") + "; "
	}
	suffix := ""
	for _, name := range names {
		if name != "_" {
			suffix += "_ = " + name + "; "
		}
	}
	if !discard && !unrelated {
		suffix += "if err != nil {return err}; "
	}
	return prefix + assignment + suffix
}

func TestPayloadEncodingGuard_SimplePackageAliases(t *testing.T) {
	source := `package sqlite
 import model "github.com/hyper-swe/mtix/internal/model"
 var marshal = (model.EncodePayload)
 var encode = marshal
 var emit = (*Store).emitPayload
 func probe() {raw,_:=((encode(nil))); _=raw; _,n:=((emit(nil,nil,nil,emitParams{},nil))),0; _=n}
 `
	hits, err := discardedPayloadErrors(source)
	require.NoError(t, err)
	require.Len(t, hits, 2)
}

func TestPayloadEncodingGuard_AllMultipleRHSHelperErrors(t *testing.T) {
	source := payloadGuardProbe("var s *Store; emit := s.emitPayload; _,n,_,m,_ := emit(nil,nil,emitParams{},nil),1,((emit(nil,nil,emitParams{},nil))),2,emit(nil,nil,emitParams{},nil); _=n;_=m")
	hits, err := discardedPayloadErrors(source)
	require.NoError(t, err)
	require.Len(t, hits, 3)
}

// Deliberately asserting an alias is outside the accidental-discard guard.
func TestPayloadEncodingGuard_KnownLimit_AssertedAlias(t *testing.T) {
	source := `package sqlite
 import "encoding/json"
 import model "github.com/hyper-swe/mtix/internal/model"
 func probe() {
  var boxed any = model.EncodePayload
  encode := boxed.(func(any)(json.RawMessage,error))
  raw,_ := encode(nil); _ = raw
 }
 `
	hits, err := discardedPayloadErrors(source)
	require.NoError(t, err)
	require.Empty(t, hits, "asserted aliases require analysis outside the documented guard scope")
}

func TestPayloadEncodingGuard_CrossFilePackageAliasesAndShadows(t *testing.T) {
	sources := map[string]string{
		"a/alias.go":      "package sample\nimport m `github.com/hyper-swe/mtix/internal/model`\nvar encode = (m.EncodePayload)",
		"a/forward.go":    "package sample\nvar forward = encode",
		"a/call.go":       "package sample\nfunc probe(){raw,_:=((forward(nil)));_=raw}",
		"a/shadow.go":     "package sample\nfunc probe(){encode:=func(any)([]byte,error){return nil,nil};raw,_:=encode(nil);_=raw}",
		"a/other_test.go": "package sample_test\nfunc encode(any)([]byte,error){return nil,nil};func probe(){raw,_:=encode(nil);_=raw}",
		"b/other.go":      "package sample\nfunc encode(any)([]byte,error){return nil,nil};func probe(){raw,_:=encode(nil);_=raw}",
	}
	globals, err := payloadPackageGlobalAliases(sources)
	require.NoError(t, err)
	for path, source := range sources {
		hits, err := discardedPayloadErrorsWithGlobals(source, globals[path])
		require.NoError(t, err)
		expected := 0
		if path == "a/call.go" {
			expected = 1
		}
		require.Len(t, hits, expected, path)
	}
}
