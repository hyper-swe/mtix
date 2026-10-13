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
	g := &historyGraph{callers: map[string][]callEdge{}, stmtInside: map[string]int{}, stmtPlain: map[string]int{}, funcsByKey: map[string]string{}}
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
			if bareCallee(x.Fun) == "withHistoryTx" {
				g.visit(x.Fun, owner, inside, strs)
				for _, arg := range x.Args {
					if lit, isLit := arg.(*ast.FuncLit); isLit {
						g.visit(lit.Body, owner, true, strs)
					} else {
						g.visit(arg, owner, inside, strs)
					}
				}
				return false
			}
			if name := bareCallee(x.Fun); name != "" {
				g.callers[name] = append(g.callers[name], callEdge{from: owner, inside: inside})
			}
		case *ast.BinaryExpr, *ast.BasicLit, *ast.Ident:
			if v, ok := foldString(x.(ast.Expr), strs); ok {
				if cnt := len(updateNodesRE.FindAllString(v, -1)); cnt > 0 {
					if inside {
						g.stmtInside[owner] += cnt
					} else {
						g.stmtPlain[owner] += cnt
					}
					return false
				}
			}
		}
		return true
	})
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
		if e.inside || e.from == "createNodeTx" {
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
