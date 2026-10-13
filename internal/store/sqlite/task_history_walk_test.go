// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
)

// callEdge is one call from a function to a callee, and whether the call sits
// inside the func-literal argument of withHistoryTx.
type callEdge struct {
	from   string
	inside bool
}

// historyGraph is the in-package call graph keyed by bare function name, plus
// where each function's UPDATE nodes statements sit.
type historyGraph struct {
	callers      map[string][]callEdge
	stmtInside   map[string]int
	stmtPlain    map[string]int
	declared     map[string]bool   // names of the package functions and methods
	funcsByKey   map[string]string // "file:Func" -> bare name
	recordedMemo map[string]bool
}

func bareCallee(e ast.Expr) string {
	switch f := e.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// buildHistoryGraph reads dir and records every call and statement position.
func buildHistoryGraph(t *testing.T, dir string) *historyGraph {
	t.Helper()
	files := parseDir(t, dir)
	strs, _ := packageStrings(files)
	g := &historyGraph{callers: map[string][]callEdge{}, stmtInside: map[string]int{}, stmtPlain: map[string]int{}, funcsByKey: map[string]string{}, declared: map[string]bool{}}
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				g.declared[fn.Name.Name] = true
			}
		}
	}
	_, decls := packageStrings(files)
	for ident, d := range decls { // package-level values may call or reference a leaf
		g.visit(d.expr, "var "+ident, false, strs)
	}
	for file, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			g.funcsByKey[file+":"+funcName(fn)] = fn.Name.Name
			g.visit(fn.Body, fn.Name.Name, false, strs)
		}
	}
	return g
}

// visit records calls and statements under n; inside is true within a
// withHistoryTx func-literal argument.
func (g *historyGraph) visit(n ast.Node, owner string, inside bool, strs map[string]string) {
	ast.Inspect(n, func(c ast.Node) bool {
		switch x := c.(type) {
		case *ast.CallExpr:
			if name := bareCallee(x.Fun); name == "withHistoryTx" || name == "inTxHistory" || name == "recordRename" {
				g.visitRecorded(x, owner, inside, strs)
				return false
			}
			g.addEdge(bareCallee(x.Fun), owner, inside)
		case *ast.Ident:
			g.addEdge(x.Name, owner, inside) // a function used as a value (f := leaf; run(leaf))
			return !g.foldedSQL(x, owner, inside, strs)
		case *ast.SelectorExpr:
			g.addEdge(x.Sel.Name, owner, inside)
		case *ast.BinaryExpr, *ast.BasicLit:
			return !g.foldedSQL(x.(ast.Expr), owner, inside, strs)
		}
		return true
	})
}

// visitRecorded visits a withHistoryTx/inTxHistory call: its func-literal
// arguments run inside the recorder.
func (g *historyGraph) visitRecorded(call *ast.CallExpr, owner string, inside bool, strs map[string]string) {
	g.visit(call.Fun, owner, inside, strs)
	for _, arg := range call.Args {
		if lit, isLit := arg.(*ast.FuncLit); isLit {
			g.visit(lit.Body, owner, true, strs)
		} else {
			g.visit(arg, owner, inside, strs)
		}
	}
}

// addEdge records that owner reaches the declared function name.
func (g *historyGraph) addEdge(name, owner string, inside bool) {
	if g.declared[name] {
		g.callers[name] = append(g.callers[name], callEdge{from: owner, inside: inside})
	}
}

// foldedSQL records an UPDATE nodes statement held by a foldable expression.
func (g *historyGraph) foldedSQL(e ast.Expr, owner string, inside bool, strs map[string]string) bool {
	v, ok := foldString(e, strs)
	return ok && g.countSQL(v, owner, inside)
}

// countSQL records the UPDATE nodes statements in folded text, and reports
// whether it found any.
func (g *historyGraph) countSQL(v, owner string, inside bool) bool {
	cnt := len(updateNodesRE.FindAllString(v, -1))
	if cnt == 0 {
		return false
	}
	if inside {
		g.stmtInside[owner] += cnt
	} else {
		g.stmtPlain[owner] += cnt
	}
	return true
}

// recorded reports whether every path to the function's statements runs
// inside withHistoryTx, or through createNodeTx, which records after it.
func (g *historyGraph) recorded(name string, onPath map[string]bool) bool {
	if g.stmtPlain[name] == 0 && g.stmtInside[name] > 0 {
		return true
	}
	onPath[name] = true
	defer delete(onPath, name)
	considered := 0
	for _, e := range g.callers[name] {
		if e.inside || e.from == "createNodeTx" || rolledBackEntry()[e.from] {
			considered++
			continue
		}
		if onPath[e.from] {
			continue // a cycle adds no new entry point
		}
		considered++
		if !g.recorded(e.from, onPath) {
			return false
		}
	}
	return considered > 0
}

// TestTaskHistoryCensus_WiredAndPendingFlags_MatchTheCallGraph checks the
// flags both ways: a wired function reached through a plain WithTx fails, and
// so does a pending one that is in fact fully recorded.
func TestTaskHistoryCensus_WiredAndPendingFlags_MatchTheCallGraph(t *testing.T) {
	g := buildHistoryGraph(t, ".")
	for key, entry := range historyCensus {
		if entry.state == censusExcluded {
			continue
		}
		name, isFunc := g.funcsByKey[key]
		got := isFunc && g.recorded(name, map[string]bool{})
		switch entry.state {
		case censusWired:
			assert.True(t, got, "%s is marked wired but a path reaches it outside withHistoryTx/createNodeTx", key)
		case censusPending:
			assert.False(t, got, "%s is marked pending A2 but every path to it is recorded: mark it wired", key)
		}
	}
}

// rolledBackEntry names the functions that run a write path in a transaction
// they always roll back (a dry run), so nothing is persisted and nothing needs
// recording.
func rolledBackEntry() map[string]bool {
	return map[string]bool{"countImportChanges": true}
}
