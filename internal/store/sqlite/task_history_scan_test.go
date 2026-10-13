// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// updateSite is one "UPDATE nodes ... SET" statement and the function (or
// package-level declaration) it belongs to.
type updateSite struct{ file, fn string }

// updateNodesRE matches the statement however the SQL is wrapped or cased.
var updateNodesRE = regexp.MustCompile(`(?is)UPDATE\s+nodes\s+SET`)

// parseDir parses the non-test Go files of dir.
func parseDir(t *testing.T, dir string) map[string]*ast.File {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	require.NoError(t, err)
	fset := token.NewFileSet()
	out := map[string]*ast.File{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		parsed, err := parser.ParseFile(fset, f, src, 0)
		require.NoError(t, err)
		out[filepath.Base(f)] = parsed
	}
	return out
}

// funcName names a declaration as Func or Type.Method.
func funcName(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	typ := d.Recv.List[0].Type
	if star, ok := typ.(*ast.StarExpr); ok {
		typ = star.X
	}
	if id, ok := typ.(*ast.Ident); ok {
		return id.Name + "." + d.Name.Name
	}
	return d.Name.Name
}

// foldString folds a constant string expression: literals, parentheses, '+'
// and identifiers naming a package-level string constant or variable.
func foldString(e ast.Expr, strs map[string]string) (string, bool) {
	switch x := e.(type) {
	case *ast.BasicLit:
		if x.Kind != token.STRING {
			return "", false
		}
		v, err := strconv.Unquote(x.Value)
		return v, err == nil
	case *ast.ParenExpr:
		return foldString(x.X, strs)
	case *ast.Ident:
		v, ok := strs[x.Name]
		return v, ok
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, lok := foldString(x.X, strs)
		r, rok := foldString(x.Y, strs)
		return l + r, lok && rok
	}
	return "", false
}

// packageStrings folds every package-level string const/var to a fixed point
// and returns the declared value expressions by name.
func packageStrings(files map[string]*ast.File) (strs map[string]string, decls map[string]declRef) {
	strs, decls = map[string]string{}, map[string]declRef{}
	for name, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if i < len(vs.Values) {
						decls[id.Name] = declRef{file: name, kind: strings.ToLower(gd.Tok.String()), expr: vs.Values[i]}
					}
				}
			}
		}
	}
	for pass := 0; pass < len(decls)+1; pass++ {
		for n, d := range decls {
			if v, ok := foldString(d.expr, strs); ok {
				strs[n] = v
			}
		}
	}
	return strs, decls
}

// declRef is a package-level declaration with a value expression.
type declRef struct {
	file, kind string
	expr       ast.Expr
}

// countStatements counts the UPDATE nodes statements an expression tree
// holds: folded '+' chains and named constants count once each.
func countStatements(root ast.Node, strs map[string]string, used map[string]bool) int {
	n := 0
	ast.Inspect(root, func(node ast.Node) bool {
		e, ok := node.(ast.Expr)
		if !ok {
			return true
		}
		switch e.(type) {
		case *ast.BinaryExpr, *ast.BasicLit, *ast.Ident:
		default:
			return true
		}
		v, folded := foldString(e, strs)
		if !folded {
			return true
		}
		ast.Inspect(e, func(inner ast.Node) bool {
			if id, isID := inner.(*ast.Ident); isID {
				used[id.Name] = true
			}
			return true
		})
		n += len(updateNodesRE.FindAllString(v, -1))
		return false
	})
	// Fragments joined at run time: the literals in source order, spaced.
	var lits []string
	ast.Inspect(root, func(node ast.Node) bool {
		if lit, ok := node.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			if v, err := strconv.Unquote(lit.Value); err == nil {
				lits = append(lits, v)
			}
		}
		return true
	})
	if joined := len(updateNodesRE.FindAllString(strings.Join(lits, " "), -1)); joined > n {
		n = joined
	}
	return n
}

// scanUpdateStatements lists every UPDATE nodes statement in the non-test
// files of dir, whitespace and case insensitive, folding concatenations and
// named constants, in function bodies, func literals and package-level
// declarations. A constant is attributed to the functions that use it, or to
// "const NAME" when nothing does.
func scanUpdateStatements(t *testing.T, dir string) []updateSite {
	t.Helper()
	files := parseDir(t, dir)
	strs, decls := packageStrings(files)
	used := map[string]bool{}
	var out []updateSite
	add := func(file, fn string, n int) {
		for i := 0; i < n; i++ {
			out = append(out, updateSite{file: file, fn: fn})
		}
	}
	for name, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				add(name, funcName(fn), countStatements(fn.Body, strs, used))
			}
		}
	}
	for ident, d := range decls {
		if _, isFunc := d.expr.(*ast.FuncLit); isFunc {
			add(d.file, "var "+ident, countStatements(d.expr, strs, used))
			continue
		}
		if v, ok := strs[ident]; ok {
			if !used[ident] {
				add(d.file, d.kind+" "+ident, len(updateNodesRE.FindAllString(v, -1)))
			}
			continue
		}
		// A composite literal (slice, map) of statements.
		add(d.file, "var "+ident, countStatements(d.expr, strs, used))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].file+out[i].fn < out[j].file+out[j].fn })
	return out
}
