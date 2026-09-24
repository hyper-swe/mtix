// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// approvalFunc is the one function of the transport package that parses
// the hub DSN (FR-18.15, MTIX-95.25).
const approvalFunc = "ApproveDSN"

// urlParseFunc is the helper that holds the package's URL parse; only
// approvalFunc may call it.
const urlParseFunc = "parseDSN"

// driverParsers maps each pgx import path to its default package name
// and to the functions that parse a connection string, either as their
// job or before they connect.
func driverParsers() map[string]struct {
	name  string
	funcs []string
} {
	return map[string]struct {
		name  string
		funcs []string
	}{
		"github.com/jackc/pgx/v5/pgconn": {"pgconn",
			[]string{"ParseConfig", "ParseConfigWithOptions", "Connect", "ConnectWithOptions"}},
		"github.com/jackc/pgx/v5": {"pgx",
			[]string{"ParseConfig", "ParseConfigWithOptions", "Connect", "ConnectWithOptions"}},
		"github.com/jackc/pgx/v5/pgxpool": {"pgxpool", []string{"ParseConfig", "New"}},
	}
}

// urlParsers lists the net/url functions that parse a URL or a query.
func urlParsers() []string { return []string{"Parse", "ParseRequestURI", "ParseQuery"} }

// parseRefs is every reference to a parsing function found in the
// transport package's non-test source, keyed by kind: "driver" (a pgx
// connection-string parser), "url" (a net/url parser) and "helper" (a
// call of urlParseFunc). Each entry is "<enclosing function>: <ref>".
type parseRefs map[string][]string

// scanTransportSource parses every non-test .go file in the package
// directory and records each reference to a parsing function.
func scanTransportSource(t *testing.T) parseRefs {
	t.Helper()
	names, err := filepath.Glob("*.go")
	require.NoError(t, err)
	refs := parseRefs{}
	fset := token.NewFileSet()
	scanned := 0
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, perr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, perr)
		scanned++
		imports := importedParsers(t, f)
		for _, decl := range f.Decls {
			scanDecl(decl, imports, refs)
		}
	}
	require.NotZero(t, scanned, "no non-test source found; the audit must run in the package directory")
	return refs
}

// importedParsers maps each local import name in f to the parsing
// functions it exposes. A dot import of a parsing package fails the
// test, since its calls could not be attributed.
func importedParsers(t *testing.T, f *ast.File) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	add := func(local, kind string, funcs []string) {
		m := map[string]string{}
		for _, fn := range funcs {
			m[fn] = kind
		}
		out[local] = m
	}
	for _, spec := range f.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		require.NoError(t, err)
		local := ""
		if spec.Name != nil {
			local = spec.Name.Name
		}
		if d, ok := driverParsers()[path]; ok {
			require.NotEqual(t, ".", local, "dot import of %s hides its parse calls", path)
			if local == "" {
				local = d.name
			}
			add(local, "driver", d.funcs)
		}
		if path == "net/url" {
			require.NotEqual(t, ".", local, "dot import of net/url hides its parse calls")
			if local == "" {
				local = "url"
			}
			add(local, "url", urlParsers())
		}
	}
	return out
}

// scanDecl records every parsing reference inside decl under the name
// of the enclosing function ("package level" outside any function).
func scanDecl(decl ast.Decl, imports map[string]map[string]string, refs parseRefs) {
	owner := "package level"
	var root ast.Node = decl
	if fd, ok := decl.(*ast.FuncDecl); ok {
		owner = fd.Name.Name
		if fd.Body == nil {
			return
		}
		root = fd.Body
	}
	ast.Inspect(root, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			pkg, ok := x.X.(*ast.Ident)
			if !ok {
				return true
			}
			if kind, found := imports[pkg.Name][x.Sel.Name]; found {
				refs[kind] = append(refs[kind], owner+": "+pkg.Name+"."+x.Sel.Name)
			}
		case *ast.Ident:
			if x.Name == urlParseFunc {
				refs["helper"] = append(refs["helper"], owner+": "+urlParseFunc)
			}
		}
		return true
	})
}

// TestTransportSource_DSNParse_OnlyInApproveDSN pins that the transport
// package parses the hub DSN in exactly one place: ApproveDSN holds the
// only pgx connection-string parse and the only call of the URL helper,
// and the helper holds the only net/url parse. The pool is then built
// from ApproveDSN's result, never from a second parse (FR-18.15,
// MTIX-95.25).
func TestTransportSource_DSNParse_OnlyInApproveDSN(t *testing.T) {
	refs := scanTransportSource(t)
	for kind := range refs {
		sort.Strings(refs[kind])
	}
	require.Equal(t, []string{approvalFunc + ": pgxpool.ParseConfig"}, refs["driver"],
		"the package must parse connection settings exactly once, in %s", approvalFunc)
	require.Equal(t, []string{approvalFunc + ": " + urlParseFunc}, refs["helper"],
		"only %s may call %s", approvalFunc, urlParseFunc)
	require.Equal(t, []string{urlParseFunc + ": url.Parse"}, refs["url"],
		"only %s may parse the DSN as a URL", urlParseFunc)
}

// transportImportPath is the import path of this package.
const transportImportPath = "github.com/hyper-swe/mtix/internal/store/postgres/transport"

// moduleRoot returns the directory holding go.mod, walking up from the
// package directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	require.NoError(t, err)
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "go.mod not found above the package directory")
		dir = parent
	}
}

// skipSourceDir reports whether the module walk skips the directory
// name: hidden directories, vendored code, web dependencies and test
// data hold no mtix non-test source.
func skipSourceDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules" || name == "testdata"
}

// transportImportNames returns every local name f gives this package
// and whether f dot-imports it.
func transportImportNames(f *ast.File) (map[string]bool, bool) {
	locals := map[string]bool{}
	dot := false
	for _, spec := range f.Imports {
		if path, err := strconv.Unquote(spec.Path.Value); err != nil || path != transportImportPath {
			continue
		}
		switch {
		case spec.Name == nil:
			locals["transport"] = true
		case spec.Name.Name == ".":
			dot = true
		default:
			locals[spec.Name.Name] = true
		}
	}
	return locals, dot
}

// enforceTLSPostureRefs returns every reference to EnforceTLSPosture in
// the non-test file f: a selector on any local name the file gives this
// package, or the bare name inside this package or under a dot import.
func enforceTLSPostureRefs(f *ast.File, inTransport bool) []string {
	locals, dot := transportImportNames(f)
	bare := inTransport || dot
	var refs []string
	for _, decl := range f.Decls {
		owner := "package level"
		var root ast.Node = decl
		if fd, ok := decl.(*ast.FuncDecl); ok {
			owner = fd.Name.Name
			if fd.Body == nil {
				continue
			}
			root = fd.Body
		}
		ast.Inspect(root, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if pkg, ok := x.X.(*ast.Ident); ok && locals[pkg.Name] && x.Sel.Name == "EnforceTLSPosture" {
					refs = append(refs, owner)
					return false
				}
			case *ast.Ident:
				if bare && x.Name == "EnforceTLSPosture" {
					refs = append(refs, owner)
				}
			}
			return true
		})
	}
	return refs
}

// TestModuleSource_EnforceTLSPosture_HasNoNonTestCaller pins that no
// non-test code in the module calls EnforceTLSPosture: a connection
// opens only from the configuration ApproveDSN approved, never from a
// DSN string (FR-18.15, MTIX-95.25).
func TestModuleSource_EnforceTLSPosture_HasNoNonTestCaller(t *testing.T) {
	root := moduleRoot(t)
	transportDir, err := filepath.Abs(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var callers []string
	scanned, sawTransport := 0, false
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipSourceDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return perr
		}
		scanned++
		inTransport := filepath.Dir(path) == transportDir
		sawTransport = sawTransport || inTransport
		rel, _ := filepath.Rel(root, path)
		for _, owner := range enforceTLSPostureRefs(f, inTransport) {
			callers = append(callers, rel+": "+owner)
		}
		return nil
	})
	require.NoError(t, walkErr)
	require.True(t, sawTransport, "the walk must include the transport package")
	require.Greater(t, scanned, 50, "the walk must cover the module's source")
	require.Empty(t, callers, "connections must open only from ApproveDSN's Config")
}
