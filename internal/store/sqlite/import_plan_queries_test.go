// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-2.3.2.2: the staged plan decides from indexed queries. Time to plan
// must not grow with the square of the board, so every statement of the plan
// (import_plan_sql.go) is pinned to the plan SQLite makes for it: a lookup
// searches an index or a key, and only the statements that are one pass over a
// table may scan. Partial indexes were the defect: SQLite uses one only for a
// query that repeats its predicate, and a bound equality does not.
//
// Per CLAUDE.md SQL Rule 1b the tests prefix exactly "EXPLAIN QUERY PLAN " to
// the package's own constant statements and bind their arguments.

// planQueryCase is one statement of import_plan_sql.go and the plan it must have.
type planQueryCase struct {
	name  string // the constant's name
	sql   string
	args  []any
	scans int    // full scans allowed: one for a statement that is a single pass
	uses  string // the index or key the plan must name, when it names one
	store bool   // the statement runs on the store, not on the staging database
}

// planQueryCases lists every statement of import_plan_sql.go.
func planQueryCases() []planQueryCase {
	row := []any{0, "id", "uid", "parent", "BIG", 0, 0, "created", "title"}
	return []planQueryCase{
		{name: "stageNodePageSQL", sql: stageNodePageSQL, args: []any{int(stageNodes), -1, stagePageRows}, uses: "PRIMARY KEY"},
		{name: "stageNodeAtSQL", sql: stageNodeAtSQL, args: []any{int(stageNodes), 0}, uses: "PRIMARY KEY"},
		{name: "planAddRowSQL", sql: planAddRowSQL, args: []any{0, 0, "", "", "", "", "", 0}},
		{name: "planTakeSQL", sql: planTakeSQL, args: []any{"k", 1}},
		{name: "planTakenSQL", sql: planTakenSQL, args: []any{"k", 1}, uses: "PRIMARY KEY"},
		{name: "planInsertIncomingSQL", sql: planInsertIncomingSQL,
			args: append(append([]any{}, row...), 0, "id", "uid", "parent", 0)},
		{name: "planIncomingByOrdSQL", sql: planIncomingByOrdSQL, args: []any{0}, uses: "PRIMARY KEY"},
		{name: "planIncomingByIDSQL", sql: planIncomingByIDSQL, args: []any{"id"}, uses: "plan_in_id"},
		{name: "planIncomingByFUIDSQL", sql: planIncomingByFUIDSQL, args: []any{"uid"}, uses: "plan_in_fuid"},
		{name: "planDuplicateUIDsSQL", sql: planDuplicateUIDsSQL, args: []any{0, 0, 0}, scans: 1, uses: "plan_in_uid"},
		{name: "planRestampSQL", sql: planRestampSQL, args: []any{"uid", 0}, uses: "PRIMARY KEY"},
		{name: "planProvOrderSQL", sql: planProvOrderSQL, scans: 1},
		{name: "planProvCountSQL", sql: planProvCountSQL, scans: 1},
		{name: "planProvAtSQL", sql: planProvAtSQL, args: []any{0}, uses: "PRIMARY KEY"},
		{name: "planRenumberInSQL", sql: planRenumberInSQL, args: []any{"id", "parent", 0, 0}, uses: "PRIMARY KEY"},
		{name: "planRebaseSetSQL", sql: planRebaseSetSQL, args: []any{"old", "new"}},
		{name: "planRebaseGetSQL", sql: planRebaseGetSQL, args: []any{"old"}, uses: "PRIMARY KEY"},
		{name: "planHintGetSQL", sql: planHintGetSQL, args: []any{"k"}, uses: "PRIMARY KEY"},
		{name: "planHintSetSQL", sql: planHintSetSQL, args: []any{"k", 1}},
		{name: "planLoadLocalSQL", sql: planLoadLocalSQL, scans: 1, store: true},
		{name: "planInsertLocalSQL", sql: planInsertLocalSQL, args: append(append([]any{}, row[:9]...), "final", 0)},
		{name: "planLocalByPosSQL", sql: planLocalByPosSQL, args: []any{0}, uses: "PRIMARY KEY"},
		{name: "planLocalByIDSQL", sql: planLocalByIDSQL, args: []any{"id"}, uses: "plan_local_id"},
		{name: "planLocalByFinalSQL", sql: planLocalByFinalSQL, args: []any{"id"}, uses: "plan_local_final"},
		{name: "planStampNextSQL", sql: planStampNextSQL, args: []any{-1}, uses: "PRIMARY KEY"},
		{name: "planStampSetSQL", sql: planStampSetSQL, args: []any{"uid", 0}, uses: "PRIMARY KEY"},
		{name: "planSaveFinalSQL", sql: planSaveFinalSQL, args: []any{"final", 0, false, 0}, uses: "PRIMARY KEY"},
		{name: "planLocalMaxSQL", sql: planLocalMaxSQL, args: []any{"BIG", "BIG-1"}, uses: "plan_local_parent"},
		{name: "planFileMaxSQL", sql: planFileMaxSQL, args: []any{"BIG", "BIG-1"}, uses: "plan_in_parent"},
		{name: "planNamespaceLocalSQL", sql: planNamespaceLocalSQL, args: []any{"BIG-1"}, uses: "plan_local_id"},
		{name: "planNamespaceInSQL", sql: planNamespaceInSQL, args: []any{"BIG-1"}, uses: "plan_in_id"},
		{name: "planCheckFinalsSQL", sql: planCheckFinalsSQL, scans: 1, uses: "plan_local_final"},
		{name: "planLocalNodeSQL", sql: planLocalNodeSQL, args: []any{"BIG-1"}, uses: "SEARCH nodes", store: true},
		{name: "planChoiceAllSQL", sql: planChoiceAllSQL, args: []any{"theirs", int(secWorkflow)}, uses: "PRIMARY KEY"},
		{name: "planChoiceTaskSQL", sql: planChoiceTaskSQL, args: []any{"theirs", int(secWorkflow), "BIG-1"}, uses: "plan_rows_c3"},
		{name: "planCountOpenSQL", sql: planCountOpenSQL, args: []any{int(secWorkflow)}, uses: "PRIMARY KEY"},
		{name: "planPageSQL", sql: planPageSQL, args: []any{int(secRemap), -1, reportPageRows}, uses: "PRIMARY KEY"},
		{name: "planRemapClearSQL", sql: planRemapClearSQL, scans: 1},
		{name: "planRemapFillSQL", sql: planRemapFillSQL, args: []any{int(secRemap), true}, uses: "PRIMARY KEY"},
		{name: "planRemapAdoptSQL", sql: planRemapAdoptSQL, args: []any{int(secAdoption)}, uses: "PRIMARY KEY"},
		{name: "planRemapPageSQL", sql: planRemapPageSQL, args: []any{"", reportPageRows}, uses: "plan_remap_uid"},
	}
}

// planQueryPlan returns the plan lines SQLite gives the statement, run against
// db with its arguments bound.
func planQueryPlan(t *testing.T, db planQuerier, tc planQueryCase) []string {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+tc.sql, tc.args...)
	require.NoError(t, err, tc.name)
	defer func() { require.NoError(t, rows.Close()) }()
	var lines []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		lines = append(lines, detail)
	}
	require.NoError(t, rows.Err())
	return lines
}

// scanLines counts the lines of a plan that scan a table in full. A constant
// row and a co-routine's result are not scans of a table.
func scanLines(plan []string) int {
	n := 0
	for _, line := range plan {
		if strings.HasPrefix(line, "SCAN ") && !strings.HasPrefix(line, "SCAN CONSTANT ROW") &&
			!strings.HasPrefix(line, "SCAN (subquery") {
			n++
		}
	}
	return n
}

// Every lookup of the plan searches an index or a key; only the statements
// that are a single pass over a table scan, and then only once. The partial
// index defect of round 1 fails planIncomingByFUIDSQL and planDuplicateUIDsSQL
// here, and the uid-less rescan fails planStampNextSQL.
func TestPlanQueries_UseAnIndex(t *testing.T) {
	s, st := planFixture(t)
	for _, tc := range planQueryCases() {
		t.Run(tc.name, func(t *testing.T) {
			var plan []string
			if tc.store {
				plan = planQueryPlan(t, s.readDB, tc)
			} else {
				plan = planQueryPlan(t, st.db, tc)
			}
			t.Logf("%s:\n  %s", tc.name, strings.Join(plan, "\n  "))
			assert.LessOrEqual(t, scanLines(plan), tc.scans, "full scans in the plan of %s", tc.name)
			if tc.uses != "" {
				assert.Contains(t, strings.Join(plan, "\n"), tc.uses, "the plan of %s must use %s", tc.name, tc.uses)
			}
		})
	}
}

// sqlConstantNames lists the statements declared in import_plan_sql.go.
func sqlConstantNames(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("import_plan_sql.go")
	require.NoError(t, err)
	var names []string
	for _, m := range regexp.MustCompile(`(?m)^\t((?:plan|stage)[A-Za-z]*SQL) = `).FindAllStringSubmatch(string(src), -1) {
		names = append(names, m[1])
	}
	sort.Strings(names)
	return names
}

// A statement added to import_plan_sql.go must be added to the table above, so
// no statement of the plan escapes the plan test.
func TestPlanQueries_EveryStatementIsPinned(t *testing.T) {
	var pinned []string
	for _, tc := range planQueryCases() {
		pinned = append(pinned, tc.name)
	}
	sort.Strings(pinned)
	require.NotEmpty(t, pinned)
	assert.Equal(t, sqlConstantNames(t), pinned)
}

// planSourceFiles lists the production files of the staged plan and its report.
func planSourceFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var files []string
	production := regexp.MustCompile(`^import_(plan|report)[a-z_]*\.go$`)
	for _, e := range entries {
		name := e.Name()
		if production.MatchString(name) && !strings.HasSuffix(name, "_test.go") {
			files = append(files, name)
		}
	}
	require.NotEmpty(t, files)
	return files
}

// No SQL of the plan or its report is built in code (CLAUDE.md SQL Rule 1): every
// statement passed to the database is a named constant of import_plan_sql.go
// (planSchemaSQL and stageSchemaSQL are constant DDL), or is chosen from those
// constants by one of the three functions below, which are a switch each.
func TestPlanQueries_NoStatementIsBuiltInCode(t *testing.T) {
	call := regexp.MustCompile(`(?:Exec|Query|QueryRow)Context\(\s*([^,]+),\s*([^,)]+)`)
	named := regexp.MustCompile(`^(?:plan|stage)[A-Za-z]*SQL$`)
	selectors := map[string]bool{"incomingQuery(key": true, "localQuery(key": true, "namespaceQuery(inFile": true}
	calls := 0
	for _, name := range planSourceFiles(t) {
		src, err := os.ReadFile(filepath.Clean(name))
		require.NoError(t, err)
		for _, m := range call.FindAllStringSubmatch(string(src), -1) {
			if strings.Contains(m[1], "context.Context") {
				continue // the method set of planQuerier
			}
			calls++
			arg := strings.TrimSpace(m[2])
			assert.True(t, named.MatchString(arg) || selectors[arg], "%s passes %q to the database: use a named constant", name, arg)
		}
		assert.NotContains(t, string(src), "Sprintf(`", name)
	}
	require.Greater(t, calls, 25, "the guard must see the calls it guards")
	sqlSrc, err := os.ReadFile("import_plan_sql.go")
	require.NoError(t, err)
	assert.NotContains(t, string(sqlSrc), "Sprintf")
}
