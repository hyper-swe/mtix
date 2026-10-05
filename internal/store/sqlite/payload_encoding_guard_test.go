// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// MTIX-107.25: prevent discarded encoder errors in production and fixtures.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
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

func isPayloadEncoder(expr ast.Expr, imports, aliases map[string]bool) bool {
	switch e := payloadUnparenthesize(expr).(type) {
	case *ast.Ident:
		return aliases[e.Name] || (imports["."] && e.Name == "EncodePayload")
	case *ast.SelectorExpr:
		if e.Sel.Name == "encodePayload" || e.Sel.Name == "encodePayloadFn" || e.Sel.Name == "emitPayload" {
			return true
		}
		owner, ok := e.X.(*ast.Ident)
		return ok && imports[owner.Name] && e.Sel.Name == "EncodePayload"
	}
	return false
}

func payloadLocalAliases(node ast.Node, imports, initial map[string]bool) map[string]bool {
	aliases := map[string]bool{}
	for name, value := range initial {
		aliases[name] = value
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
				if id, ok := names[i].(*ast.Ident); ok && !aliases[id.Name] && isPayloadEncoder(value, imports, aliases) {
					aliases[id.Name] = true
					changed = true
				}
			}
			return true
		})
	}
	return aliases
}

func discardedPayloadErrors(source string) ([]token.Position, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "source.go", source, 0)
	if err != nil {
		return nil, err
	}
	imports := payloadEncoderAliases(file)
	globals := map[string]bool{}
	for _, decl := range file.Decls {
		if _, ok := decl.(*ast.GenDecl); ok {
			globals = payloadLocalAliases(decl, imports, globals)
		}
	}
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

func payloadDiscardPositions(node ast.Node, fset *token.FileSet, imports, aliases map[string]bool) []token.Position {
	var hits []token.Position
	ast.Inspect(node, func(n ast.Node) bool {
		call, discarded := payloadDiscardCall(n)
		if discarded && call != nil && isPayloadEncoder(call.Fun, imports, aliases) {
			hits = append(hits, fset.Position(call.Pos()))
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

func payloadDiscardCall(n ast.Node) (*ast.CallExpr, bool) {
	var call *ast.CallExpr
	discarded := false
	switch a := n.(type) {
	case *ast.AssignStmt:
		if len(a.Rhs) == 1 {
			call, _ = payloadUnparenthesize(a.Rhs[0]).(*ast.CallExpr)
		}
		if len(a.Lhs) == 1 || len(a.Lhs) == 2 {
			if id, ok := a.Lhs[len(a.Lhs)-1].(*ast.Ident); ok {
				discarded = id.Name == "_"
			}
		}
	case *ast.ValueSpec:
		if len(a.Values) == 1 {
			call, _ = payloadUnparenthesize(a.Values[0]).(*ast.CallExpr)
		}
		if len(a.Names) == 1 || len(a.Names) == 2 {
			discarded = a.Names[len(a.Names)-1].Name == "_"
		}
	case *ast.ExprStmt:
		call, _ = payloadUnparenthesize(a.X).(*ast.CallExpr)
		discarded = true
	case *ast.GoStmt:
		call = a.Call
		discarded = true
	case *ast.DeferStmt:
		call = a.Call
		discarded = true
	}
	return call, discarded
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
	require.NoError(t, filepath.WalkDir("../..", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hits, err := discardedPayloadErrors(string(source))
		if err != nil {
			return err
		}
		for _, hit := range hits {
			t.Errorf("%s:%d: discarded payload encoder error", path, hit.Line)
		}
		return nil
	}))
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
