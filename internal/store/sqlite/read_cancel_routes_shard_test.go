// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// sqliteTestShards is the number of hash shards CI splits this package into
// (.github/workflows/ci.yml test-go-sqlite; scripts/ci-go-shards.sh).
const sqliteTestShards = 3

// readRouteTests maps each route family swept by a top-level
// TestReadRoutes_* test to the shard scripts/ci-go-shards.sh gives that test
// (MTIX-264). Shards are chosen by djb2(test name) mod 3 + 1, and subtests are
// never split, so one test that sweeps every route lands wholly in one shard
// and made macOS shard 1 run 1017-1138 s against its 1200 s limit. Each route
// is its own top-level test so the hash spreads the cost; renaming a test
// moves it, so TestReadRouteShards_Assignment_SpreadsAcrossAllShards fails
// until this table is updated to a name that keeps the spread.
//
//	shard 1: TestReadFamilies_CancelledMidQuery_LeaveNoStaleSnapshot (the 12 short
//	         families), StoreQuery, InboxAckInfo
//	shard 2: ReadDBQueryContext, ReadDBConnPrepareContext, ConnRawPrepare
//	shard 3: StoreQueryRow, ConnPrepareContext, TxPrepareContext
var readRouteTests = map[string]struct {
	family     string
	shard      int
	iterations int
}{
	"TestReadRoutes_StoreQuery_CancelledMidRead_LeaveNoStaleSnapshot":               {"Store.Query", 1, 20000},
	"TestReadRoutes_InboxAckInfo_CancelledMidRead_LeaveNoStaleSnapshot":             {"InboxAckInfo", 1, 20000},
	"TestReadRoutes_ReadDBQueryContext_CancelledMidRead_LeaveNoStaleSnapshot":       {"ReadDB.QueryContext", 2, 20000},
	"TestReadRoutes_ReadDBConnPrepareContext_CancelledMidRead_LeaveNoStaleSnapshot": {"ReadDB.Conn.PrepareContext", 2, 20000},
	"TestReadRoutes_ConnRawPrepare_CancelledMidRead_LeaveNoStaleSnapshot":           {"Conn.Raw.Prepare", 2, 20000},
	"TestReadRoutes_StoreQueryRow_CancelledMidRead_LeaveNoStaleSnapshot":            {"Store.QueryRow", 3, 20000},
	"TestReadRoutes_ConnPrepareContext_CancelledMidRead_LeaveNoStaleSnapshot":       {"Conn.PrepareContext", 3, 20000},
	"TestReadRoutes_TxPrepareContext_CancelledMidRead_LeaveNoStaleSnapshot":         {"Tx.PrepareContext", 3, 20000},
}

// readFamiliesTestName is the test that keeps the short families.
const readFamiliesTestName = "TestReadFamilies_CancelledMidQuery_LeaveNoStaleSnapshot"

// shardOfTestName mirrors assign_shards in scripts/ci-go-shards.sh: djb2 over
// the alphabet 0-9A-Z_a-z (1-based index), modulo 2^32, then mod shards + 1.
func shardOfTestName(name string, shards int) int {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcdefghijklmnopqrstuvwxyz"
	h := uint64(5381)
	for _, c := range name {
		h = (h*33 + uint64(strings.IndexRune(alphabet, c)) + 1) % 4294967296
	}
	return int(h%uint64(shards)) + 1
}

// declaredRouteTests lists the top-level TestReadRoutes_* functions in the
// package's route test file.
func declaredRouteTests(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "read_cancel_routes_test.go", nil, 0)
	require.NoError(t, err)
	var names []string
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "TestReadRoutes_") {
			names = append(names, fn.Name.Name)
		}
	}
	sort.Strings(names)
	return names
}

func TestReadRouteShards_Assignment_SpreadsAcrossAllShards(t *testing.T) {
	perShard := map[int]int{}
	for name, want := range readRouteTests {
		require.Equal(t, want.shard, shardOfTestName(name, sqliteTestShards),
			"%s moved shard; update the table in this file and pick a name that keeps the spread", name)
		perShard[want.shard]++
	}
	perShard[shardOfTestName(readFamiliesTestName, sqliteTestShards)]++
	for s := 1; s <= sqliteTestShards; s++ {
		require.GreaterOrEqual(t, perShard[s], 2, "shard %d must carry at least two of the read-cancel tests: %v", s, perShard)
		require.LessOrEqual(t, perShard[s], 4, "shard %d is overloaded with read-cancel tests: %v", s, perShard)
	}
}

func TestReadRouteShards_RouteTests_MatchTheRouteFamilies(t *testing.T) {
	var want []string
	for name := range readRouteTests {
		want = append(want, name)
	}
	sort.Strings(want)
	require.Equal(t, want, declaredRouteTests(t), "every route test must be declared and listed in the shard table")

	families := map[string]bool{}
	for _, r := range readRouteTests {
		families[r.family] = true
	}
	for name := range cancelReadFamilies(nil) {
		require.Equal(t, families[name], sweepsRoute(name), "family %q: sweepsRoute and the route tests disagree", name)
	}
}

// routeTestBodies maps each top-level TestReadRoutes_* function to its body's
// statements, parsed from read_cancel_routes_test.go.
func routeTestBodies(t *testing.T) map[string][]ast.Stmt {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "read_cancel_routes_test.go", nil, 0)
	require.NoError(t, err)
	bodies := map[string][]ast.Stmt{}
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "TestReadRoutes_") {
			bodies[fn.Name.Name] = fn.Body.List
		}
	}
	return bodies
}

func TestReadRouteShards_RouteTests_AreBoundToTheirTableEntry(t *testing.T) {
	require.Len(t, readRouteTests, 8, "the table must hold all eight routes")
	require.Equal(t, 20000, readCancelRouteIterations)
	families := map[string]bool{}
	for name, e := range readRouteTests {
		require.Equal(t, 20000, e.iterations, "%s must sweep 20000 iterations", name)
		require.False(t, families[e.family], "family %q is listed twice", e.family)
		families[e.family] = true
		require.True(t, sweepsRoute(e.family), "%s: %q is not a route family", name, e.family)
	}
	bodies := routeTestBodies(t)
	require.Len(t, bodies, len(readRouteTests))
	for name, stmts := range bodies {
		require.Len(t, stmts, 1, "%s must only call sweepRouteByTestName(t)", name)
		call, ok := stmts[0].(*ast.ExprStmt).X.(*ast.CallExpr)
		require.True(t, ok, "%s must only call sweepRouteByTestName(t)", name)
		fun, ok := call.Fun.(*ast.Ident)
		require.True(t, ok && fun.Name == "sweepRouteByTestName", "%s must call sweepRouteByTestName", name)
		require.Len(t, call.Args, 1, name)
		arg, ok := call.Args[0].(*ast.Ident)
		require.True(t, ok && arg.Name == "t", "%s must pass only t", name)
	}
}

func TestReadFamilies_ShortList_ExcludesTheRoutesAndTestUsesIt(t *testing.T) {
	short := shortReadFamilyNames()
	for _, e := range readRouteTests {
		require.NotContains(t, short, e.family, "route %q would run twice", e.family)
	}
	require.Len(t, short, len(cancelReadFamilies(nil))-len(readRouteTests))
	f, err := parser.ParseFile(token.NewFileSet(), "read_cancel_families_test.go", nil, 0)
	require.NoError(t, err)
	used := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != readFamiliesTestName {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "shortReadFamilyNames" {
					used = true
				}
			}
			return true
		})
	}
	require.True(t, used, "%s must iterate shortReadFamilyNames()", readFamiliesTestName)
}
