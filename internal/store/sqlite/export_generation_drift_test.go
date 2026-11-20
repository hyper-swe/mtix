// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// exportReaderTables returns every table the export snapshot reads, derived
// from the source of EVERY non-test file in the package: it starts at
// readExportSnapshotWith, follows each call to a function of this package, and
// to a method of the receiver's own type, by name, and collects the tables after FROM and JOIN in the SELECT
// string literals of those functions and of the string constants they name. A
// new read in a new file or a new JOIN is therefore found as soon as the export
// path reaches it (MTIX-130.20.2).
func exportReaderTables(t *testing.T) []string {
	t.Helper()
	funcs, consts := parsePackageSource(t)
	return readerTablesFrom(funcs, consts, "readExportSnapshotWith")
}

// readerTablesFrom walks the functions reachable from root and returns the
// tables their SELECTs read. A plain function is reached by a call or by being
// named as a value; a method is reached through a selector on the enclosing
// function's receiver or a parameter of the same type, as a call or as a method
// value, so a read hidden behind a function value is still found. A selector on
// anything else (a standard-library value, a field) never reaches a method of
// this package that happens to share its name (MTIX-130.20.2).
func readerTablesFrom(funcs map[string][]*ast.FuncDecl, consts map[string]string, root string) []string {
	seen := map[*ast.FuncDecl]bool{}
	var queue []*ast.FuncDecl
	enqueue := func(fn *ast.FuncDecl) {
		if !seen[fn] {
			seen[fn] = true
			queue = append(queue, fn)
		}
	}
	for _, fn := range funcs[root] {
		enqueue(fn)
	}
	tables := map[string]bool{}
	for len(queue) > 0 {
		fn := queue[0]
		queue = queue[1:]
		vars := receiverTypes(fn)
		var visit func(n ast.Node) bool
		visit = func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.SelectorExpr:
				x, isIdent := v.X.(*ast.Ident)
				if isIdent {
					for _, m := range funcs[v.Sel.Name] {
						if want := vars[x.Name]; m.Recv != nil && want != "" && want == typeName(m.Recv.List[0].Type) {
							enqueue(m)
						}
					}
				}
				ast.Inspect(v.X, visit)
				return false
			case *ast.BasicLit:
				addTables(tables, literalText(v))
			case *ast.Ident:
				for _, f := range funcs[v.Name] {
					if f.Recv == nil {
						enqueue(f)
					}
				}
				addTables(tables, consts[v.Name])
			}
			return true
		}
		ast.Inspect(fn.Body, visit)
	}
	out := make([]string, 0, len(tables))
	for table := range tables {
		out = append(out, table)
	}
	sort.Strings(out)
	return out
}

// receiverTypes maps the receiver and parameter names of fn to their type
// names, so a selector on one of them can be matched to a method of that type.
func receiverTypes(fn *ast.FuncDecl) map[string]string {
	vars := map[string]string{}
	lists := []*ast.FieldList{fn.Type.Params}
	if fn.Recv != nil {
		lists = append(lists, fn.Recv)
	}
	for _, list := range lists {
		if list == nil {
			continue
		}
		for _, field := range list.List {
			for _, name := range field.Names {
				vars[name.Name] = typeName(field.Type)
			}
		}
	}
	return vars
}

// typeName returns the name of a plain or pointer type, and "" for any other
// type expression.
func typeName(e ast.Expr) string {
	if star, ok := e.(*ast.StarExpr); ok {
		e = star.X
	}
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// parsePackageSource parses every non-test .go file of the package and returns
// its functions and methods by name and its package-level string constants.
func parsePackageSource(t *testing.T) (map[string][]*ast.FuncDecl, map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	funcs := map[string][]*ast.FuncDecl{}
	exprs := map[string]ast.Expr{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, parseErr, name)
		indexDecls(file, funcs, exprs)
	}
	return funcs, resolveConsts(exprs)
}

// indexDecls adds the functions of a file to funcs by name and its
// package-level value declarations to exprs by name.
func indexDecls(file *ast.File, funcs map[string][]*ast.FuncDecl, exprs map[string]ast.Expr) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				funcs[d.Name.Name] = append(funcs[d.Name.Name], d)
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok && len(vs.Values) == len(vs.Names) {
					for i, ident := range vs.Names {
						exprs[ident.Name] = vs.Values[i]
					}
				}
			}
		}
	}
}

// resolveConsts flattens every constant expression to its text, repeating until
// no constant changes, so a constant built from one declared later, in the same
// file or in a later file, resolves. The pass count is bounded by the number of
// constants, which also ends a cycle (MTIX-130.20.2).
func resolveConsts(exprs map[string]ast.Expr) map[string]string {
	consts := map[string]string{}
	for pass := 0; pass <= len(exprs); pass++ {
		changed := false
		for name, e := range exprs {
			if text := constText(e, consts); text != consts[name] {
				consts[name] = text
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return consts
}

// constText flattens a string constant expression (literals joined with +,
// names of constants resolved so far) into its text; anything else is empty.
// resolveConsts repeats it until nothing changes.
func constText(e ast.Expr, consts map[string]string) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		return literalText(v)
	case *ast.Ident:
		return consts[v.Name]
	case *ast.BinaryExpr:
		return constText(v.X, consts) + constText(v.Y, consts)
	case *ast.ParenExpr:
		return constText(v.X, consts)
	}
	return ""
}

func literalText(l *ast.BasicLit) string {
	if l.Kind != token.STRING {
		return ""
	}
	text, err := strconv.Unquote(l.Value)
	if err != nil {
		return ""
	}
	return text
}

// addTables records the tables a SELECT statement reads after FROM or JOIN.
func addTables(tables map[string]bool, sqlText string) {
	tableRef := regexp.MustCompile(`(?is)\b(?:FROM|JOIN)\s+([a-z_][a-z0-9_]*)`)
	if !strings.Contains(strings.ToUpper(sqlText), "SELECT") {
		return
	}
	for _, m := range tableRef.FindAllStringSubmatch(sqlText, -1) {
		tables[strings.ToLower(m[1])] = true
	}
}

func TestExportChangeToken_TriggersCoverExactlyTheExportedTables(t *testing.T) {
	declared := exportedTables()
	require.True(t, sort.StringsAreSorted(declared), "keep exportedTables sorted")

	s := newInternalTestStore(t)
	rows, err := s.readDB.QueryContext(context.Background(),
		`SELECT name, tbl_name FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'export_gen_%'`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	ops := map[string][]string{}
	for rows.Next() {
		var name, table string
		require.NoError(t, rows.Scan(&name, &table))
		ops[table] = append(ops[table], name[strings.LastIndex(name, "_")+1:])
	}
	require.NoError(t, rows.Err())
	var triggered []string
	for table, got := range ops {
		triggered = append(triggered, table)
		sort.Strings(got)
		require.Equal(t, []string{"ad", "ai", "au"}, got, "table %s needs insert, update and delete triggers", table)
	}
	sort.Strings(triggered)
	require.Equal(t, declared, triggered, "the triggered tables must be exactly exportedTables")
	require.Equal(t, declared, exportReaderTables(t), "the tables the export reads must be exactly exportedTables")
}

// The walk finds a table read through a function or method named as a value,
// not only through a call, and through a call by name (MTIX-130.20.2).
func TestExportReaderTables_FollowsFunctionValuesAndCalls(t *testing.T) {
	const src = `package p
const q = "SELECT a FROM via_const"
const early = "SELECT a " + late
const late = "FROM via_later_const"
func (s *S) root() { f := viaValue; _ = f; g := s.viaMethod; _ = g; viaCall(); _ = early; _ = other.unrelatedMethod; _ = s.unrelated }
func viaValue() { _ = "SELECT a FROM from_value" }
func (s *S) viaMethod() { _ = "SELECT a JOIN from_method" }
func viaCall() { _ = q }
func unrelated() { _ = "SELECT a FROM never_reached" }
func (o *O) unrelatedMethod() { _ = "SELECT a FROM wrong_receiver" }
`
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, 0)
	require.NoError(t, err)
	funcs := map[string][]*ast.FuncDecl{}
	exprs := map[string]ast.Expr{}
	indexDecls(file, funcs, exprs)
	require.Equal(t, []string{"from_method", "from_value", "via_const", "via_later_const"},
		readerTablesFrom(funcs, resolveConsts(exprs), "root"))
}

// A constant cycle ends instead of looping, and resolves to nothing.
func TestResolveConsts_Cycle_Terminates(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", `package p
const a = b + "x"
const b = a + "y"
`, 0)
	require.NoError(t, err)
	exprs := map[string]ast.Expr{}
	indexDecls(file, map[string][]*ast.FuncDecl{}, exprs)
	require.NotPanics(t, func() { resolveConsts(exprs) })
}
