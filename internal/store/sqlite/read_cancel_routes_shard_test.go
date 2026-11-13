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

// sqliteTestShards is the number of hash shards CI splits this package into
// (.github/workflows/ci.yml test-go-sqlite; scripts/ci-go-shards.sh).
const sqliteTestShards = 4

// readRouteTests maps each route family swept by a top-level
// TestReadRoutes_* test to the shard scripts/ci-go-shards.sh gives that test
// (MTIX-264, MTIX-270). Shards are chosen by djb2(test name) mod 4 + 1, and
// subtests are never split, so each route is its own top-level test and the
// hash places its cost. MTIX-270 moved the SQLite race run from three shards
// to four because three shards left one shard within 15 percent of the 20
// minute package limit (macOS 989/620/1033 s). A route costs about 50 s on
// Ubuntu and 65 s on macOS, the families test about 20 s, so the eight routes
// are spread two per shard and the families test sits with shard 3's two
// routes. Under four shards the unrenamed names put five routes on shard 2, so
// three tests were renamed (StoreQueryRow, ConnPrepareContext,
// TxPrepareContext) to names that also keep the remote bar's three-shard split
// at MTIX-264's 3/0/5 (remoteBarRoutesPerShard). Renaming a test moves it, so
// TestReadRouteShards_Assignment_FollowsMeasuredShardLoad fails until the
// table is updated to a name that keeps this placement.
//
//	shard 1: StoreQueryRow, InboxAckInfo
//	shard 2: StoreQuery, ReadDBQuery
//	shard 3: ReadDBConnPrepareCtx, ConnPrepareContext, and the 12 short families
//	         (TestReadFamilies_CancelledMidQuery_LeaveNoStaleSnapshot)
//	shard 4: ConnRawPrepareStmt, TxPrepareContext
var readRouteTests = map[string]struct {
	family     string
	shard      int
	iterations int
}{
	"TestReadRoutes_StoreQuery_CancelledMidRead_LeaveNoStaleSnapshot":              {"Store.Query", 2, 20000},
	"TestReadRoutes_InboxAckInfo_CancelledMidRead_LeaveNoStaleSnapshot":            {"InboxAckInfo", 1, 20000},
	"TestReadRoutes_ReadDBQuery_CancelledMidRead_LeaveNoStaleSnapshot":             {"ReadDB.QueryContext", 2, 20000},
	"TestReadRoutes_ReadDBConnPrepareCtx_CancelledMidRead_LeaveNoStaleSnapshot":    {"ReadDB.Conn.PrepareContext", 3, 20000},
	"TestReadRoutes_ConnRawPrepareStmt_CancelledMidRead_LeaveNoStaleSnapshot":      {"Conn.Raw.Prepare", 4, 20000},
	"TestReadRoutes_StoreQueryRowPool_CancelledMidRead_LeaveNoStaleSnapshot":       {"Store.QueryRow", 1, 20000},
	"TestReadRoutes_ConnPrepareContextRoute_CancelledMidRead_LeaveNoStaleSnapshot": {"Conn.PrepareContext", 3, 20000},
	"TestReadRoutes_TxPrepareContextMain_CancelledMidRead_LeaveNoStaleSnapshot":    {"Tx.PrepareContext", 4, 20000},
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

// wantRoutesPerShard is the placement by measured route cost (MTIX-270): two
// routes on each of the four shards.
var wantRoutesPerShard = map[int]int{1: 2, 2: 2, 3: 2, 4: 2}

func TestReadRouteShards_Assignment_FollowsMeasuredShardLoad(t *testing.T) {
	perShard := map[int]int{1: 0, 2: 0, 3: 0, 4: 0}
	for name, want := range readRouteTests {
		require.Equal(t, want.shard, shardOfTestName(name, sqliteTestShards),
			"%s moved shard; update the table in this file and pick a name that keeps the placement", name)
		perShard[want.shard]++
	}
	require.Equal(t, wantRoutesPerShard, perShard, "route tests per shard (two on each)")
	require.Equal(t, 3, shardOfTestName(readFamiliesTestName, sqliteTestShards), "the short families test stays on shard 3")
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

// remoteBarRoutesPerShard is the read-cancel placement under the remote bar's
// three SQLite shards (MTIX-264 pinned 3/0/5 there). MTIX-270 moved ci.yml to
// four shards and chose the route names so that this split is unchanged.
var remoteBarRoutesPerShard = map[int]int{1: 3, 2: 0, 3: 5}

func TestReadRouteShards_ShardCounts_MatchTheWorkflows(t *testing.T) {
	ci, bar := workflowShardCounts(t)
	require.Equal(t, sqliteTestShards, ci, "ci.yml test-go-sqlite shard count and sqliteTestShards must change together")
	require.Equal(t, map[int]bool{3: true}, bar, "remote-bar.yml SQLite shard count changed: update remoteBarRoutesPerShard and the route names")
}

func TestReadRouteShards_RemoteBarSplit_KeepsTheMeasuredThreeShardPlacement(t *testing.T) {
	_, bar := workflowShardCounts(t)
	require.Len(t, bar, 1)
	perShard := map[int]int{1: 0, 2: 0, 3: 0}
	for name := range readRouteTests {
		perShard[shardOfTestName(name, 3)]++
	}
	require.Equal(t, remoteBarRoutesPerShard, perShard, "route tests per remote-bar shard (3 / 0 / 5, shard 2 holds none)")
	require.Equal(t, 1, shardOfTestName(readFamiliesTestName, 3), "the families test stays on remote-bar shard 1")
}
