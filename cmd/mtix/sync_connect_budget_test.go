// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// connectBudgetName is the package constant every hub connect timeout
// uses (MTIX-95.7).
const connectBudgetName = "syncConnectBudget"

// packageSource is the parsed non-test source of this package: every
// function declaration by name, the file that holds it, and each file.
type packageSource struct {
	fset  *token.FileSet
	funcs map[string]*ast.FuncDecl
	files map[string]*ast.File
}

// parsePackageSource parses every non-test .go file in the package
// directory.
func parsePackageSource(t *testing.T) packageSource {
	t.Helper()
	src := packageSource{fset: token.NewFileSet(), funcs: map[string]*ast.FuncDecl{}, files: map[string]*ast.File{}}
	names, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, readErr := os.ReadFile(name) //nolint:gosec // package source file from Glob
		require.NoError(t, readErr)
		f, parseErr := parser.ParseFile(src.fset, name, body, parser.SkipObjectResolution)
		require.NoError(t, parseErr)
		src.files[name] = f
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil {
				src.funcs[fd.Name.Name] = fd
			}
		}
	}
	require.NotEmpty(t, src.files, "the package source must be found")
	return src
}

// render prints an expression as gofmt would.
func (s packageSource) render(t *testing.T, e ast.Expr) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, printer.Fprint(&buf, s.fset, e))
	return buf.String()
}

// constValue returns the printed value of the package-level constant
// name, or "" when no such constant is declared.
func (s packageSource) constValue(t *testing.T, name string) string {
	t.Helper()
	for _, f := range s.files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, id := range vs.Names {
					if id.Name == name && i < len(vs.Values) {
						return s.render(t, vs.Values[i])
					}
				}
			}
		}
	}
	return ""
}

// timeoutBudgets returns the printed timeout argument of every
// context.WithTimeout call under root.
func (s packageSource) timeoutBudgets(t *testing.T, root ast.Node) []string {
	t.Helper()
	var out []string
	ast.Inspect(root, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WithTimeout" {
			return true
		}
		if pkg, isIdent := sel.X.(*ast.Ident); isIdent && pkg.Name == "context" {
			out = append(out, s.render(t, call.Args[1]))
		}
		return true
	})
	return out
}

// TestDoctor_ConnectBudgetMatchesSync pins that mtix sync doctor gives
// every hub connection the connect budget sync init, clone, push and pull
// use: one package constant of 30 s, named at every connect timeout, with
// no other duration left in a doctor file. A hub that is resuming from
// idle therefore passes doctor whenever it syncs (MTIX-95.7).
func TestDoctor_ConnectBudgetMatchesSync(t *testing.T) {
	src := parsePackageSource(t)
	require.Equal(t, "30 * time.Second", src.constValue(t, connectBudgetName),
		"%s must be declared once, as 30 * time.Second", connectBudgetName)

	sites := []struct {
		fn   string
		role string
	}{
		{"runSyncInit", "sync init"},
		{"runSyncClone", "sync clone"},
		{"runSyncPush", "sync push"},
		{"runSyncPull", "sync pull"},
		{"checkPGReachable", "doctor: PG reachable"},
		{"checkSchemaCurrent", "doctor: schema current"},
		{"verifyHubPrivileges", "doctor: hub-privileges"},
		{"readHubObjects", "doctor: hub-triggers"},
	}
	for _, site := range sites {
		t.Run(site.role, func(t *testing.T) {
			fd := src.funcs[site.fn]
			require.NotNilf(t, fd, "%s (%s) not found", site.fn, site.role)
			budgets := src.timeoutBudgets(t, fd.Body)
			require.NotEmptyf(t, budgets, "%s sets no connect timeout", site.fn)
			for _, b := range budgets {
				require.Equalf(t, connectBudgetName, b, "%s must connect within %s", site.fn, connectBudgetName)
			}
		})
	}

	var doctorFiles []string
	for name := range src.files {
		if strings.HasPrefix(name, "sync_doctor") {
			doctorFiles = append(doctorFiles, name)
		}
	}
	sort.Strings(doctorFiles)
	require.NotEmpty(t, doctorFiles)
	for _, name := range doctorFiles {
		t.Run("no other timeout in "+name, func(t *testing.T) {
			f := src.files[name]
			for _, b := range src.timeoutBudgets(t, f) {
				require.Equalf(t, connectBudgetName, b, "%s: every doctor timeout is the connect budget", name)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				if be, ok := n.(*ast.BinaryExpr); ok {
					require.NotEqualf(t, "10 * time.Second", src.render(t, be),
						"%s keeps a 10 s literal", name)
				}
				return true
			})
		})
	}
}
