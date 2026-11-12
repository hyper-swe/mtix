// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// readPoolGuardMinFiles is the least number of non-test sources a scan must
// see; fewer means the glob matched the wrong directory and the guard would
// pass for the wrong reason.
const readPoolGuardMinFiles = 100

// readPoolBypasses returns one line per place a non-test source reaches the
// reader pool other than through readPool and its shield (MTIX-250): any use
// of the raw handle field rawDB outside read_pool.go (through an alias or any
// variable, not only s.readDB), a readPool built outside read_pool.go, a
// Store.readDB that is not a *readPool, another Store field holding a raw
// *sql.DB, and a prepare, statement or raw-connection call on anything derived
// from the pool (a Conn, a transaction, an alias, a ReadPool parameter). It
// errors when given too few files to mean anything.
func readPoolBypasses(files map[string]string) ([]string, error) {
	if len(files) < readPoolGuardMinFiles {
		return nil, os.ErrInvalid
	}
	var out []string
	fset := token.NewFileSet()
	for name, text := range files {
		file, err := parser.ParseFile(fset, name, text, parser.SkipObjectResolution)
		if err != nil {
			out = append(out, name+": cannot parse, so the read guard cannot judge it")
			continue
		}
		tainted := poolDerivedNames(file)
		ast.Inspect(file, func(n ast.Node) bool {
			out = append(out, readPoolBypassIn(fset, name, n, tainted)...)
			return true
		})
	}
	sort.Strings(out)
	return out, nil
}

// readPoolPrepareCalls are the methods that give a statement or the driver
// connection of a reader connection or transaction; the shield wraps the
// statements it returns, but a source outside the shield must not rely on that.
var readPoolPrepareCalls = map[string]bool{
	"PrepareContext": true, "Prepare": true, "StmtContext": true, "Stmt": true, "Raw": true,
}

// poolRoot reports whether e is the reader pool or something derived from it:
// the readDB field, a ReadDB() or reader() call, a name already known to hold
// such a value, or any call, selector or index on one of those.
func poolRoot(e ast.Expr, tainted map[string]bool) bool {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name == "readDB" || poolRoot(x.X, tainted)
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && (sel.Sel.Name == "ReadDB" || sel.Sel.Name == "reader") {
			return true
		}
		return poolRoot(x.Fun, tainted)
	case *ast.Ident:
		return tainted[x.Name]
	case *ast.StarExpr:
		return poolRoot(x.X, tainted)
	case *ast.ParenExpr:
		return poolRoot(x.X, tainted)
	case *ast.UnaryExpr:
		return poolRoot(x.X, tainted)
	}
	return false
}

// poolDerivedNames returns the identifiers of the file that hold the reader
// pool or a value derived from it: names assigned from such an expression, and
// parameters or receivers typed readPool or ReadPool. It repeats until no new
// name appears, so a chain of aliases is followed. Names are file wide, which
// is coarse and errs toward reporting.
func poolDerivedNames(file *ast.File) map[string]bool {
	tainted := map[string]bool{}
	for changed := true; changed; {
		changed = false
		mark := func(id *ast.Ident) {
			if id.Name != "_" && !tainted[id.Name] {
				tainted[id.Name] = true
				changed = true
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			for _, id := range poolHolders(n, tainted) {
				mark(id)
			}
			return true
		})
	}
	return tainted
}

// poolHolders returns the identifiers one node makes hold the reader pool or
// a value derived from it: the left side of an assignment or the names of a
// var from such an expression, and parameters typed readPool or ReadPool.
func poolHolders(n ast.Node, tainted map[string]bool) []*ast.Ident {
	var out []*ast.Ident
	switch x := n.(type) {
	case *ast.AssignStmt:
		for _, r := range x.Rhs {
			if !poolRoot(r, tainted) {
				continue
			}
			for _, l := range x.Lhs {
				if id, ok := l.(*ast.Ident); ok {
					out = append(out, id)
				}
			}
		}
	case *ast.ValueSpec:
		for _, v := range x.Values {
			if poolRoot(v, tainted) {
				out = append(out, x.Names...)
			}
		}
	case *ast.Field:
		if strings.Contains(exprString(x.Type), "eadPool") {
			out = append(out, x.Names...)
		}
	}
	return out
}

// readPoolBypassIn judges one syntax node of the file name.
func readPoolBypassIn(fset *token.FileSet, name string, n ast.Node, tainted map[string]bool) []string {
	home := filepath.Base(name) == "read_pool.go"
	pos := func(p token.Pos) string { return name + ":" + strconv.Itoa(fset.Position(p).Line) }
	switch x := n.(type) {
	case *ast.SelectorExpr, *ast.CallExpr:
		return accessBypassIn(pos, x, home, tainted)
	case *ast.CompositeLit:
		if id, ok := x.Type.(*ast.Ident); ok && id.Name == "readPool" && !home {
			return []string{pos(x.Pos()) + ": readPool built outside read_pool.go"}
		}
	case *ast.TypeSpec:
		if st, ok := x.Type.(*ast.StructType); ok && x.Name.Name == "Store" {
			return storeReaderFields(pos, st)
		}
	}
	return nil
}

// accessBypassIn judges a selector or call: any .rawDB outside read_pool.go
// unwraps a pool whatever the variable, and a prepare, statement or raw call on
// a value derived from the pool is a way around the shield.
func accessBypassIn(pos func(token.Pos) string, n ast.Node, home bool, tainted map[string]bool) []string {
	if home {
		return nil
	}
	switch x := n.(type) {
	case *ast.SelectorExpr:
		if x.Sel.Name == "rawDB" {
			return []string{pos(x.Pos()) + ": rawDB unwraps the reader pool"}
		}
	case *ast.CallExpr:
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok && readPoolPrepareCalls[sel.Sel.Name] && poolRoot(sel.X, tainted) {
			return []string{pos(x.Pos()) + ": " + sel.Sel.Name + " on a value derived from the reader pool"}
		}
	}
	return nil
}

// storeReaderFields checks the Store struct: readDB is a *readPool, and no
// other field holds a raw *sql.DB that could serve reads.
func storeReaderFields(pos func(token.Pos) string, st *ast.StructType) []string {
	var out []string
	found := false
	for _, f := range st.Fields.List {
		for _, nm := range f.Names {
			t := exprString(f.Type)
			switch {
			case nm.Name == "readDB":
				found = true
				if t != "*readPool" {
					out = append(out, pos(f.Pos())+": Store.readDB is "+t+", not *readPool")
				}
			case nm.Name != "writeDB" && t == "*sql.DB":
				out = append(out, pos(f.Pos())+": Store."+nm.Name+" holds a raw *sql.DB beside the reader pool")
			}
		}
	}
	if !found {
		out = append(out, pos(st.Pos())+": Store has no readDB field")
	}
	return out
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return "*" + exprString(x.X)
	case *ast.SelectorExpr:
		return exprString(x.X) + "." + x.Sel.Name
	case *ast.Ident:
		return x.Name
	}
	return "?"
}

// TestSourceScan_NoReaderPoolBypass_InNonTestFiles fails if a non-test source
// of this package reaches the reader pool without the readPool choke point
// (MTIX-250), so a new read cannot reintroduce the stale-snapshot exposure.
func TestSourceScan_NoReaderPoolBypass_InNonTestFiles(t *testing.T) {
	files := map[string]string{}
	paths, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: a source file of this package found by Glob.
		require.NoError(t, err)
		files[p] = string(b)
	}
	got, err := readPoolBypasses(files)
	require.NoError(t, err, "the scan matched too few files to mean anything")
	require.Empty(t, got)
}

// TestReadPoolBypasses_BypassingSources_AreReported proves the guard is not
// vacuous: each bypass form is reported on a synthetic source, and a source
// that only uses the pool is not (MTIX-250).
func TestReadPoolBypasses_BypassingSources_AreReported(t *testing.T) {
	filler := map[string]string{}
	for i := 0; i < readPoolGuardMinFiles; i++ {
		filler["f"+strconv.Itoa(i)+".go"] = "package sqlite\n"
	}
	cases := []struct {
		name, src, want string
	}{
		{"unwrap", "package sqlite\nfunc f(s *Store) { _ = s.readDB.rawDB }\n", "rawDB unwraps the reader pool"},
		{"alias unwrap", "package sqlite\nfunc f(s *Store) { p := s.readDB; _ = p.rawDB }\n", "rawDB unwraps the reader pool"},
		{"alias chain unwrap", "package sqlite\nfunc f(s *Store) { p := s.readDB; q := p; _ = q.rawDB }\n", "rawDB unwraps the reader pool"},
		{"exported alias unwrap", "package sqlite\nfunc f(s *Store) { r := s.ReadDB(); _ = r.rawDB }\n", "rawDB unwraps the reader pool"},
		{"param unwrap", "package sqlite\nfunc f(p *ReadPool) { _ = p.rawDB }\n", "rawDB unwraps the reader pool"},
		{"var decl unwrap", "package sqlite\nfunc f(s *Store) { var p = s.readDB; _ = p.rawDB }\n", "rawDB unwraps the reader pool"},
		{"conn prepare", "package sqlite\nfunc f(s *Store) { c, _ := s.readDB.Conn(nil); c.PrepareContext(nil, \"\") }\n", "PrepareContext on a value derived"},
		{"exported conn prepare", "package sqlite\nfunc f(s *Store) { c, _ := s.ReadDB().Conn(nil); c.Prepare(\"\") }\n", "Prepare on a value derived"},
		{"conn raw", "package sqlite\nfunc f(s *Store) { c, _ := s.readDB.Conn(nil); c.Raw(nil) }\n", "Raw on a value derived"},
		{"tx prepare", "package sqlite\nfunc f(s *Store) { tx, _ := s.readDB.BeginTx(nil, nil); tx.PrepareContext(nil, \"\") }\n", "PrepareContext on a value derived"},
		{"tx stmt", "package sqlite\nfunc f(s *Store) { tx, _ := s.readDB.BeginTx(nil, nil); tx.StmtContext(nil, nil) }\n", "StmtContext on a value derived"},
		{"param prepare", "package sqlite\nfunc f(p *ReadPool) { c, _ := p.Conn(nil); c.PrepareContext(nil, \"\") }\n", "PrepareContext on a value derived"},
		{"direct prepare", "package sqlite\nfunc f(s *Store) { s.readDB.PrepareContext(nil, \"\") }\n", "PrepareContext on a value derived"},
		{"build", "package sqlite\nvar p = readPool{}\n", "readPool built outside read_pool.go"},
		{"field type", "package sqlite\ntype Store struct{ readDB *sql.DB }\n", "not *readPool"},
		{"second handle", "package sqlite\ntype Store struct{ readDB *readPool; other *sql.DB }\n", "holds a raw *sql.DB"},
		{"no field", "package sqlite\ntype Store struct{ x int }\n", "no readDB field"},
		{"clean", "package sqlite\ntype Store struct{ readDB *readPool; writeDB *sql.DB }\nfunc f(s *Store) { _ = s.readDB.QueryRowContext }\n", ""},
		{"writer prepare is not the pool", "package sqlite\nfunc f(s *Store) { c, _ := s.writeDB.Conn(nil); c.PrepareContext(nil, \"\") }\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{"bad.go": tc.src}
			for k, v := range filler {
				files[k] = v
			}
			got, err := readPoolBypasses(files)
			require.NoError(t, err)
			if tc.want == "" {
				require.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			require.Contains(t, got[0], tc.want)
		})
	}
	_, err := readPoolBypasses(map[string]string{"one.go": "package sqlite\n"})
	require.Error(t, err, "a scan of too few files is refused")
}
