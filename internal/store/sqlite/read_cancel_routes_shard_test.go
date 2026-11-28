// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
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

// sqliteTestShards is the number of shards CI and the remote bar split this
// package into (.github/workflows/ci.yml test-go-sqlite, remote-bar.yml
// bar-shard and bar-macos-shard; scripts/ci-go-shards.sh).
const sqliteTestShards = 6

// sqliteShardPinFile pins this package's heaviest tests to a shard
// (MTIX-131.2); scripts/ci-go-shards.sh reads it.
const sqliteShardPinFile = "../../../scripts/ci-go-shard-pins/internal-store-sqlite.txt"

// readRouteTests maps each route family swept by a top-level
// TestReadRoutes_* test to the shard its pin gives that test (MTIX-264,
// MTIX-270, MTIX-131.2). Subtests are never split, so each route is its own
// top-level test. MTIX-131.2 moved the package to six shards and pinned the
// sweeps by measured cost (a route costs about 80 to 115 s under -race on a
// three-core box, the families test about 137 s): at most two routes per
// shard, none on shard 6, which runs the writer-read sweep alone.
//
//	shard 1: StoreQuery
//	shard 2: ConnRawPrepareStmt, StoreQueryRowPool
//	shard 3: InboxAckInfo, ReadDBConnPrepareCtx
//	shard 4: TxPrepareContextMain, and the short families
//	         (TestReadFamilies_CancelledMidQuery_LeaveNoStaleSnapshot)
//	shard 5: ReadDBQuery, ConnPrepareContextRoute
var readRouteTests = map[string]struct {
	family     string
	shard      int
	iterations int
}{
	"TestReadRoutes_StoreQuery_CancelledMidRead_LeaveNoStaleSnapshot":              {"Store.Query", 1, 20000},
	"TestReadRoutes_InboxAckInfo_CancelledMidRead_LeaveNoStaleSnapshot":            {"InboxAckInfo", 3, 20000},
	"TestReadRoutes_ReadDBQuery_CancelledMidRead_LeaveNoStaleSnapshot":             {"ReadDB.QueryContext", 5, 20000},
	"TestReadRoutes_ReadDBConnPrepareCtx_CancelledMidRead_LeaveNoStaleSnapshot":    {"ReadDB.Conn.PrepareContext", 3, 20000},
	"TestReadRoutes_ConnRawPrepareStmt_CancelledMidRead_LeaveNoStaleSnapshot":      {"Conn.Raw.Prepare", 2, 20000},
	"TestReadRoutes_StoreQueryRowPool_CancelledMidRead_LeaveNoStaleSnapshot":       {"Store.QueryRow", 2, 20000},
	"TestReadRoutes_ConnPrepareContextRoute_CancelledMidRead_LeaveNoStaleSnapshot": {"Conn.PrepareContext", 5, 20000},
	"TestReadRoutes_TxPrepareContextMain_CancelledMidRead_LeaveNoStaleSnapshot":    {"Tx.PrepareContext", 4, 20000},
}

// readFamiliesTestName is the test that keeps the short families.
const readFamiliesTestName = "TestReadFamilies_CancelledMidQuery_LeaveNoStaleSnapshot"

// pinnedShards reads the pin file: its shard count and every pinned test's
// shard ("shards <N> [hash <H>]", then "<TestName> <shard>", # comments).
func pinnedShards(t *testing.T) (int, map[string]int) {
	t.Helper()
	raw, err := os.ReadFile(sqliteShardPinFile)
	require.NoError(t, err)
	count := 0
	pins := map[string]int{}
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		require.GreaterOrEqual(t, len(f), 2, line)
		n, err := strconv.Atoi(f[1])
		require.NoError(t, err, line)
		if f[0] == "shards" {
			count = n
			continue
		}
		pins[f[0]] = n
	}
	return count, pins
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

// wantRoutesPerShard is the placement by measured route cost (MTIX-131.2):
// at most two routes per shard, none on the writer-read shard 6.
var wantRoutesPerShard = map[int]int{1: 1, 2: 2, 3: 2, 4: 1, 5: 2, 6: 0}

func TestReadRouteShards_Assignment_FollowsMeasuredShardLoad(t *testing.T) {
	count, pins := pinnedShards(t)
	require.Equal(t, sqliteTestShards, count, "the pin file and sqliteTestShards must change together")
	perShard := map[int]int{1: 0, 2: 0, 3: 0, 4: 0, 5: 0, 6: 0}
	for name, want := range readRouteTests {
		got, ok := pins[name]
		require.True(t, ok, "%s must be pinned in the shard pin file", name)
		require.Equal(t, want.shard, got, "%s moved shard; update the table in this file", name)
		perShard[want.shard]++
	}
	require.Equal(t, wantRoutesPerShard, perShard, "route tests per shard")
	require.Equal(t, 4, pins[readFamiliesTestName], "the short families test stays on shard 4")
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
	require.Equal(t, 20000, readCancelStressIterations)
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

// workflowShardCounts reads the SQLite shard counts the workflows use: ci.yml
// (the test-check line of test-go-sqlite) and remote-bar.yml (the shards value
// of every internal/store/sqlite matrix entry).
func workflowShardCounts(t *testing.T) (ci int, bar map[int]bool) {
	t.Helper()
	raw, err := os.ReadFile("../../../.github/workflows/ci.yml")
	require.NoError(t, err)
	m := regexp.MustCompile(`test-check internal/store/sqlite (\d+)`).FindAllStringSubmatch(string(raw), -1)
	require.Len(t, m, 1, "ci.yml must hold exactly one SQLite test-check line")
	ci, err = strconv.Atoi(m[0][1])
	require.NoError(t, err)
	raw, err = os.ReadFile("../../../.github/workflows/remote-bar.yml")
	require.NoError(t, err)
	bar = map[int]bool{}
	for _, e := range regexp.MustCompile(`pkg: internal/store/sqlite, shards: "(\d+)"`).FindAllStringSubmatch(string(raw), -1) {
		n, err := strconv.Atoi(e[1])
		require.NoError(t, err)
		bar[n] = true
	}
	return ci, bar
}

func TestReadRouteShards_ShardCounts_MatchTheWorkflows(t *testing.T) {
	ci, bar := workflowShardCounts(t)
	require.Equal(t, sqliteTestShards, ci, "ci.yml test-go-sqlite shard count and sqliteTestShards must change together")
	require.Equal(t, map[int]bool{sqliteTestShards: true}, bar, "remote-bar.yml runs the same SQLite shard count as ci.yml")
}
