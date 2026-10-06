// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// This syntax guard catches accidental direct calls and simple aliases.
// Deliberate asserted/type-switched aliases, reflection, and reassignment
// through closures, conditionals or loops remain the seq3345 review limit.
func TestSyncApplyClockGuard_ProductionPaths_NoDirectOrAliasedNow(t *testing.T) {
	paths, err := filepath.Glob("sync_apply*.go")
	require.NoError(t, err)
	paths = append(paths, "dependency.go", "../postgres/transport/push_presence.go")
	for _, path := range paths {
		if filepath.Ext(path) != ".go" || len(path) >= 8 && path[len(path)-8:] == "_test.go" {
			continue
		}
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		file, err := parser.ParseFile(token.NewFileSet(), path, raw, 0)
		require.NoError(t, err)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if path == "dependency.go" && fn.Name.Name != "autoBlockNode" && fn.Name.Name != "autoUnblockNode" && fn.Name.Name != "restoreUnblockedNode" {
				continue
			}
			require.Empty(t, directClockCalls(file, fn.Body), "%s:%s must use the injected clock", path, fn.Name.Name)
		}
	}
}

func clockImports(file *ast.File) map[string]bool {
	imports := map[string]bool{}
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil || path != "time" {
			continue
		}
		name := "time"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		imports[name] = true
	}
	return imports
}

// clockScope tracks ordinary lexical aliases, without evaluating control flow.
type clockScope struct {
	parent  *clockScope
	aliases map[string]bool
}

func (s *clockScope) lookup(name string) (bool, bool) {
	for scope := s; scope != nil; scope = scope.parent {
		if alias, ok := scope.aliases[name]; ok {
			return alias, true
		}
	}
	return false, false
}

func clockReference(expr ast.Expr, imports map[string]bool, scope *clockScope) bool {
	switch v := expr.(type) {
	case *ast.ParenExpr:
		return clockReference(v.X, imports, scope)
	case *ast.SelectorExpr:
		id, ok := v.X.(*ast.Ident)
		if !ok {
			return false
		}
		_, shadowed := scope.lookup(id.Name)
		return !shadowed && imports[id.Name] && v.Sel.Name == "Now"
	case *ast.Ident:
		if alias, known := scope.lookup(v.Name); known {
			return alias
		}
		return imports["."] && v.Name == "Now"
	}
	return false
}

func clockAliasBindings(node ast.Node) ([]*ast.Ident, []ast.Expr) {
	switch v := node.(type) {
	case *ast.ValueSpec:
		return v.Names, v.Values
	case *ast.AssignStmt:
		ids := make([]*ast.Ident, len(v.Lhs))
		for i, lhs := range v.Lhs {
			ids[i], _ = lhs.(*ast.Ident)
		}
		return ids, v.Rhs
	}
	return nil, nil
}

func packageClockScope(file *ast.File, imports map[string]bool) *clockScope {
	scope := &clockScope{aliases: map[string]bool{}}
	// Monotone over finite package bindings, including forward alias references.
	for changed := true; changed; {
		changed = false
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.VAR {
				continue
			}
			for _, spec := range gen.Specs {
				names, values := clockAliasBindings(spec)
				for i, value := range values {
					if i >= len(names) {
						continue
					}
					name := names[i].Name
					if !scope.aliases[name] && clockReference(value, imports, scope) {
						scope.aliases[name], changed = true, true
					}
				}
			}
		}
	}
	return scope
}

func scanClockNode(node ast.Node, imports map[string]bool, scope *clockScope, hits *[]token.Pos) {
	ast.Inspect(node, func(child ast.Node) bool {
		if block, ok := child.(*ast.BlockStmt); ok {
			inner := &clockScope{parent: scope, aliases: map[string]bool{}}
			for _, stmt := range block.List {
				scanClockNode(stmt, imports, inner, hits)
			}
			return false
		}
		names, values := clockAliasBindings(child)
		for i, name := range names {
			if name == nil {
				continue
			}
			alias := i < len(values) && clockReference(values[i], imports, scope)
			target := scope
			if assignment, ok := child.(*ast.AssignStmt); ok && assignment.Tok == token.ASSIGN {
				for target.parent != nil {
					if _, found := target.aliases[name.Name]; found {
						break
					}
					target = target.parent
				}
			}
			target.aliases[name.Name] = alias
		}
		if call, ok := child.(*ast.CallExpr); ok && clockReference(call.Fun, imports, scope) {
			*hits = append(*hits, call.Pos())
		}
		return true
	})
}

func directClockCalls(file *ast.File, body ast.Node) []token.Pos {
	imports := clockImports(file)
	scope := packageClockScope(file, imports)
	var hits []token.Pos
	if body == file {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				scanClockNode(fn.Body, imports, scope, &hits)
			}
		}
	} else {
		scanClockNode(body, imports, scope, &hits)
	}
	return hits
}

func TestSyncApplyClockGuard_OrdinaryCallsAndAliases(t *testing.T) {
	cases := []struct {
		name, imports, declarations string
		want                        int
	}{
		{"direct", `"time"`, "func f(){ time.Now() }", 1},
		{"renamed raw import", "tm `time`", "func f(){ tm.Now() }", 1},
		{"escaped import", `tm "t\x69me"`, "func f(){ tm.Now() }", 1},
		{"dot import", `. "time"`, "func f(){ Now() }", 1},
		{"local chain", `"time"`, "func f(){ now := time.Now; clock := (now); (clock)() }", 1},
		{"package chain", `"time"`, "var clock = now; var now = time.Now; func f(){ clock() }", 1},
		{"multiple RHS", `"time"`, "func f(){ x, now := 1, time.Now; _ = x; now() }", 1},
		{"injection reference", `"time"`, "func f(){ use(time.Now) }; func use(func() time.Time){}", 0},
		{"unrelated method", `"time"`, "type c struct{}; func(c) Now(){}; func f(){ c{}.Now() }", 0},
		{"documented asserted alias limit", `"time"`, "func f(){ now := any(time.Now).(func() time.Time); now() }", 0},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "sample.go", "package sample\nimport "+tt.imports+"\n"+tt.declarations, 0)
			require.NoError(t, err)
			require.Len(t, directClockCalls(file, file), tt.want)
		})
	}
}
