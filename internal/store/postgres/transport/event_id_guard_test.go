// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// eventIDName matches an identifier or field that holds a sync event id.
var eventIDName = regexp.MustCompile(`(?i)event_?ids?`)

// textSinks are the calls whose arguments reach an operator or a log.
var textSinks = map[string]bool{
	"fmt.Errorf": true, "fmt.Sprintf": true, "fmt.Sprint": true,
	"errors.New": true, "slog.String": true, "slog.Any": true,
}

// rawEventIDArgs returns a position for every argument of a text sink in
// src whose expression mentions an event id without being exactly a
// format.EventID call (MTIX-187.4.4). The guard is positive: wrapping the
// id in string(), fmt.Sprint, strconv.Quote, a concatenation, a local
// identifier or strings.Join does not get it past.
//
// This guard is syntactic and cannot be complete: it follows names within a
// package, not values through function returns or types. It is a best-effort
// tripwire. The primary guarantee is the behavioural tests that drive hostile
// ids through each error path and assert the rendered text.
func rawEventIDArgs(t *testing.T, filename, src string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	require.NoError(t, err)
	return rawEventIDArgsIn(fset, []*ast.File{file})
}

// rawEventIDArgsIn scans the files of one package. A name is an event id when
// it matches eventIDName or when a local is assigned or ranged from an
// expression that mentions one (MTIX-187.4.11: the relay publisher's loop
// variable id), found by a fixpoint over the package so that a value built in
// one function and ranged over in another is followed.
func rawEventIDArgsIn(fset *token.FileSet, files []*ast.File) []string {
	tainted := taintedNames(files)
	var bad []string
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isTextSink(call) {
				return true
			}
			for _, arg := range call.Args {
				if mentionsEventID(arg, tainted) && !isFormatEventID(arg) && !isLenCall(arg) {
					bad = append(bad, fset.Position(arg.Pos()).String())
				}
			}
			return true
		})
	}
	return bad
}

// taintedNames returns the local names bound, directly or through other
// bound names, to an expression that mentions an event id.
func taintedNames(files []*ast.File) map[string]bool {
	tainted := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, file := range files {
			ast.Inspect(file, func(n ast.Node) bool {
				changed = bindTaint(tainted, n) || changed
				return true
			})
		}
	}
	return tainted
}

// bindTaint taints the names an assignment or range binds from an event id
// expression, and reports whether it tainted a new one.
func bindTaint(tainted map[string]bool, n ast.Node) bool {
	changed := false
	switch x := n.(type) {
	case *ast.AssignStmt:
		for i, l := range x.Lhs {
			if len(x.Rhs) == 1 && len(x.Lhs) > 1 && i == len(x.Lhs)-1 && isStatusName(l) {
				continue // a, err := f(...) and v, ok := f(...): the status result is no id
			}
			changed = markTainted(tainted, l, x.Rhs) || changed
		}
	case *ast.RangeStmt:
		// Both the key (a map's ids) and the value are bound from the ranged expression.
		for _, l := range []ast.Expr{x.Key, x.Value} {
			if l != nil {
				changed = markTainted(tainted, l, []ast.Expr{x.X}) || changed
			}
		}
	}
	return changed
}

// isStatusName reports the conventional name of a final error or bool result.
func isStatusName(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && (id.Name == "err" || id.Name == "ok" || id.Name == "found")
}

// indexKeys returns the index expressions of an assignment target: m[id] = 1
// binds the id into m.
func indexKeys(e ast.Expr) []ast.Expr {
	var out []ast.Expr
	for {
		switch x := e.(type) {
		case *ast.IndexExpr:
			out = append(out, x.Index)
			e = x.X
		case *ast.SelectorExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		default:
			return out
		}
	}
}

// taintKey is the name an assignment target binds: x.field for a field of a
// local, the root identifier otherwise (rendered for rendered[i]).
func taintKey(e ast.Expr) string {
	if sel, ok := e.(*ast.SelectorExpr); ok {
		if root, isIdent := sel.X.(*ast.Ident); isIdent {
			return root.Name + "." + sel.Sel.Name
		}
	}
	if id := rootIdent(e); id != nil {
		return id.Name
	}
	return ""
}

// rootIdent returns the identifier an assignment target is rooted at:
// rendered for rendered[i], x for x.field.
func rootIdent(e ast.Expr) *ast.Ident {
	for {
		switch x := e.(type) {
		case *ast.Ident:
			return x
		case *ast.IndexExpr:
			e = x.X
		case *ast.SelectorExpr:
			e = x.X
		case *ast.ParenExpr:
			e = x.X
		case *ast.StarExpr:
			e = x.X
		default:
			return nil
		}
	}
}

func markTainted(tainted map[string]bool, lhs ast.Expr, rhs []ast.Expr) bool {
	name := taintKey(lhs)
	if name == "" || name == "_" || name == "err" || tainted[name] {
		return false
	}
	if len(rhs) == 1 && isFormatEventID(rhs[0]) {
		return false // out[i] = format.EventID(id): the target holds rendered text
	}
	for _, r := range append(append([]ast.Expr{}, rhs...), indexKeys(lhs)...) {
		if isMakeCall(r) || isLenCall(r) || isFormatEventID(r) {
			continue // an empty container, a count or a rendered id holds no raw id
		}
		if mentionsEventID(r, tainted) {
			tainted[name] = true
			return true
		}
	}
	return false
}

// logMethods are the method names of loggers; a call of one on any receiver
// is a sink (p.log.Warn, logger.Info).
var logMethods = map[string]bool{"Debug": true, "Info": true, "Warn": true, "Error": true, "Log": true}

func isTextSink(call *ast.CallExpr) bool {
	if textSinks[calleeName(call)] {
		return true
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && logMethods[sel.Sel.Name]
}

func calleeName(call *ast.CallExpr) string {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return pkg.Name + "." + sel.Sel.Name
}

func isFormatEventID(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	return ok && calleeName(call) == "format.EventID"
}

// isLenCall reports a len(...) or idBytes(...) call: a count of ids names no id.
func isLenCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && (id.Name == "len" || id.Name == "idBytes")
}

func isMakeCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	return ok && id.Name == "make"
}

// mentionsEventID reports whether e holds an event id: a name that matches
// eventIDName or is tainted, other than under a format.EventID call. A field
// x.Sel holds an id when Sel is named like one or when x.Sel itself was bound
// to one (a field-qualified name); other fields of a tainted x do not.
func mentionsEventID(e ast.Expr, tainted map[string]bool) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.CallExpr:
			if isFormatEventID(x) {
				return false
			}
		case *ast.Ident:
			found = eventIDName.MatchString(x.Name) || tainted[x.Name]
		case *ast.SelectorExpr:
			found = eventIDName.MatchString(x.Sel.Name)
			if root, onIdent := x.X.(*ast.Ident); onIdent {
				found = found || tainted[root.Name+"."+x.Sel.Name]
				return false
			}
		}
		return !found
	})
	return found
}

// TestTransportErrors_NeverTextRawEventIDs: no text sink in this package's
// production code receives an event id except through format.EventID.
func TestTransportErrors_NeverTextRawEventIDs(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, rerr := os.ReadFile(f)
		require.NoError(t, rerr)
		require.Empty(t, rawEventIDArgs(t, f, string(src)), "raw event id in a text sink of %s", f)
	}
}

// TestEventIDGuard_FlagsEveryBypass: the guard fails each way of getting a
// raw id into text, and passes the rendered form.
func TestEventIDGuard_FlagsEveryBypass(t *testing.T) {
	cases := []struct {
		name string
		call string
		bad  bool
	}{
		{"selector", `fmt.Errorf("x %s", e.EventID)`, true},
		{"string conversion", `fmt.Errorf("x %s", string(e.EventID))`, true},
		{"fmt.Sprint", `fmt.Errorf("x %s", fmt.Sprint(e.EventID))`, true},
		{"strconv.Quote", `fmt.Errorf("x %s", strconv.Quote(e.EventID))`, true},
		{"concatenation", `fmt.Errorf("x %s", "id="+e.EventID)`, true},
		{"local identifier", `fmt.Errorf("x %s", eventID)`, true},
		{"strings.Join", `fmt.Errorf("x %s", strings.Join(eventIDs, ", "))`, true},
		{"Sprintf sink", `fmt.Sprintf("x %s", e.HeldEventID)`, true},
		{"errors.New sink", `errors.New(e.EventID)`, true},
		{"slog sink", `slog.String("event_id", eventID)`, true},
		{"rendered", `fmt.Errorf("x %s", format.EventID(e.EventID))`, false},
		{"count of ids", `fmt.Errorf("x %d", len(eventIDs))`, false},
		{"unrelated", `fmt.Errorf("x %d", n)`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := rawEventIDArgs(t, "snippet.go", "package p\nvar _ = "+c.call+"\n")
			require.Equal(t, c.bad, len(bad) > 0, "%s", c.call)
		})
	}
}

// TestRelayErrors_NeverTextRawEventIDs extends the guard to the relay
// packages (MTIX-187.4.11): no text sink in their production code receives an
// event id except through format.EventID.
func TestRelayErrors_NeverTextRawEventIDs(t *testing.T) {
	root := filepath.Join("..", "..", "..", "relay")
	dirs := map[string][]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			dirs[filepath.Dir(path)] = append(dirs[filepath.Dir(path)], path)
		}
		return nil
	}))
	require.GreaterOrEqual(t, len(dirs), 5, "the guard must see the relay packages")
	scanned := 0
	for dir, paths := range dirs {
		fset := token.NewFileSet()
		var files []*ast.File
		for _, p := range paths {
			f, err := parser.ParseFile(fset, p, nil, 0)
			require.NoError(t, err)
			files = append(files, f)
			scanned++
		}
		require.Empty(t, rawEventIDArgsIn(fset, files), "raw event id in a text sink of %s", dir)
	}
	require.Greater(t, scanned, 15, "the guard must scan the relay files")
}

// TestEventIDGuard_FollowsLocalBindings: a local assigned or ranged from an
// event id expression is an event id, in the same function or another one
// of the package.
func TestEventIDGuard_FollowsLocalBindings(t *testing.T) {
	cases := []struct {
		name string
		src  string
		bad  bool
	}{
		{"range over a list of ids", "func f(ids []string, e E) {\n\tids = append(ids, e.EventID)\n\tfor _, id := range ids {\n\t\t_ = fmt.Sprintf(\"x %s\", id)\n\t}\n}", true},
		{"binding followed across functions", "func g(e E) (ids []string) { ids = append(ids, e.EventID); return }\nfunc f() {\n\tids := g(E{})\n\tfor i, id := range ids {\n\t\t_, _ = i, fmt.Errorf(\"x %s\", id)\n\t}\n}", true},
		{"short assignment", "func f(e E) {\n\tid := e.EventID\n\t_ = fmt.Errorf(\"x %s\", id)\n}", true},
		{"rendered local", "func f(e E) {\n\tid := e.EventID\n\t_ = fmt.Errorf(\"x %s\", format.EventID(id))\n}", false},
		{"map key range", "func f(m map[string]int, e E) {\n\tm[e.EventID] = 1\n\tfor id := range m {\n\t\t_ = fmt.Errorf(\"x %s\", id)\n\t}\n}", true},
		{"index assignment", "func f(e E) {\n\tout := make([]string, 1)\n\tout[0] = e.EventID\n\t_ = fmt.Errorf(\"x %s\", out)\n}", true},
		{"rendered index assignment", "func f(e E) {\n\tout := make([]string, 1)\n\tout[0] = format.EventID(e.EventID)\n\t_ = fmt.Errorf(\"x %s\", out)\n}", false},
		{"multi-result call", "func f(e E) {\n\tbefore, after, _ := strings.Cut(e.EventID, \":\")\n\t_ = fmt.Errorf(\"x %s\", after)\n\t_ = before\n}", true},
		{"status result is not an id", "func f(e E) {\n\tv, err := lookup(e.EventID)\n\t_ = fmt.Errorf(\"x %w\", err)\n\t_ = v\n}", false},
		{"found result is not an id", "func f(e E) {\n\tv, found := lookup(e.EventID)\n\t_ = fmt.Errorf(\"x %v\", found)\n\t_ = v\n}", false},
		{"map returned by a lookup", "func f(e E, l L) {\n\tids := []string{e.EventID}\n\tseqs := l.Lookup(ids)\n\tfor k := range seqs {\n\t\t_ = fmt.Errorf(\"x %s\", k)\n\t}\n}", true},
		{"local identity helper", "func passID(s string) string { return s }\nfunc f(e E) {\n\tv := passID(e.EventID)\n\t_ = fmt.Errorf(\"x %s\", v)\n}", true},
		{"strconv helper", "func f(e E) {\n\tq := strconv.Quote(e.EventID)\n\t_ = fmt.Errorf(\"x %s\", q)\n}", true},
		{"field bound to an id", "func f(e E, w W) {\n\tw.ref = e.EventID\n\t_ = fmt.Errorf(\"x %s\", w.ref)\n}", true},
		{"other field of the same value", "func f(e E, w W) {\n\tw.ref = e.EventID\n\t_ = fmt.Errorf(\"x %s\", w.name)\n}", false},
		{"bool result is not an id", "func f(e E) {\n\tv, ok := lookup(e.EventID)\n\t_ = fmt.Errorf(\"x %v\", ok)\n\t_ = v\n}", false},
		{"logger method sink", "func f(l L, e E) {\n\tl.Warn(\"x\", e.EventID)\n}", true},
		{"logger method with rendered id", "func f(l L, e E) {\n\tl.Warn(\"x\", format.EventID(e.EventID))\n}", false},
		{"taint needing several passes", "func f(c string) {\n\t_ = fmt.Errorf(\"x %s\", c)\n}\nfunc g() {\n\tc := b\n\tf(c)\n}\nfunc h() {\n\tb := a\n\t_ = b\n}\nfunc k(e E) {\n\ta := e.EventID\n\t_ = a\n}", true},
		{"unrelated loop", "func f(names []string) {\n\tfor _, n := range names {\n\t\t_ = fmt.Errorf(\"x %s\", n)\n\t}\n}", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := rawEventIDArgs(t, "snippet.go", "package p\ntype E struct{ EventID string }\ntype L struct{}\ntype W struct{ ref, name string }\n"+c.src+"\n")
			require.Equal(t, c.bad, len(bad) > 0, "%s", c.src)
		})
	}
}
