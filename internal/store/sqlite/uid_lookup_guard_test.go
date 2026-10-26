// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The guard for lookups by uid (MTIX-95.47). idx_nodes_uid and
// idx_sync_events_uid are partial indexes (schema.go: WHERE uid IS NOT NULL
// AND uid <> ''), and SQLite uses a partial index only for a query whose
// WHERE implies the index's. A bound "uid = ?" implies "uid IS NOT NULL" but
// not "uid <> ''", so a lookup without that term reads every row, or every
// row another index finds. These tests read every string literal of the
// package's production source and fail on a lookup by uid that does not
// carry the term.

// uidIndexedTable is a table whose lookups by uid the guard checks, and its
// partial uid index.
type uidIndexedTable struct {
	name, index string
	// boundOnly counts only lookups by a bound value (uid = ?, uid IN (?)).
	// The uid of sync_events is also compared with the uid of nodes in joins
	// that reach sync_events by event_id, and the text of a join cannot tell
	// which side SQLite searches, so such a comparison is not counted.
	boundOnly bool
}

var (
	nodesUIDTable      = uidIndexedTable{name: "nodes", index: "idx_nodes_uid"}
	syncEventsUIDTable = uidIndexedTable{name: "sync_events", index: "idx_sync_events_uid", boundOnly: true}
)

// sqlLiteral is one string literal of the package's production source.
type sqlLiteral struct {
	pos   string // file:line:column
	fn    string // enclosing function ("resolveNodeRef", "Store.loadSettleTarget"); "" outside one
	query string // the literal's value; literals joined by + are one
}

// packageSQLLiterals returns every string literal of the package's
// non-test Go files, read from source (the test runs in the package
// directory).
func packageSQLLiterals(t *testing.T) []sqlLiteral {
	t.Helper()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var out []sqlLiteral
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		require.NoError(t, err, "parse %s", name)
		for _, decl := range f.Decls {
			lits, err := literalsIn(fset, decl)
			require.NoError(t, err, "read the literals of %s", name)
			out = append(out, lits...)
		}
	}
	require.NotEmpty(t, out, "the package source must be readable from the test's directory")
	return out
}

// literalsIn returns the string literals of decl, a + of literals as one.
func literalsIn(fset *token.FileSet, decl ast.Decl) ([]sqlLiteral, error) {
	fn := funcKey(decl)
	var out []sqlLiteral
	var firstErr error
	ast.Inspect(decl, func(n ast.Node) bool {
		expr, ok := n.(ast.Expr)
		if !ok || firstErr != nil {
			return firstErr == nil
		}
		text, ok, err := constString(expr)
		if err != nil {
			firstErr = fmt.Errorf("%s: %w", fset.Position(expr.Pos()), err)
			return false
		}
		if !ok {
			return true
		}
		out = append(out, sqlLiteral{pos: fset.Position(expr.Pos()).String(), fn: fn, query: text})
		return false
	})
	return out, firstErr
}

// funcKey names the function decl declares: "name", or "Type.name" for a
// method; "" for any other declaration.
func funcKey(decl ast.Decl) string {
	fd, ok := decl.(*ast.FuncDecl)
	if !ok {
		return ""
	}
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	typ := fd.Recv.List[0].Type
	if star, isStar := typ.(*ast.StarExpr); isStar {
		typ = star.X
	}
	if id, isIdent := typ.(*ast.Ident); isIdent {
		return id.Name + "." + fd.Name.Name
	}
	return fd.Name.Name
}

// constString returns the value of expr when it is a string literal or a +
// of string literals.
func constString(expr ast.Expr) (string, bool, error) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false, nil
		}
		v, err := strconv.Unquote(e.Value)
		if err != nil {
			return "", false, fmt.Errorf("unquote %s: %w", e.Value, err)
		}
		return v, true, nil
	case *ast.ParenExpr:
		return constString(e.X)
	case *ast.BinaryExpr:
		if e.Op != token.ADD {
			return "", false, nil
		}
		l, lok, err := constString(e.X)
		if err != nil || !lok {
			return "", false, err
		}
		r, rok, err := constString(e.Y)
		if err != nil || !rok {
			return "", false, err
		}
		return l + r, true, nil
	}
	return "", false, nil
}

var (
	// sqlCommentRE matches an SQL comment, whose prose can mention uid.
	sqlCommentRE = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/`)
	// setClauseRE matches the assignments of an UPDATE, up to its WHERE:
	// they write uid, they do not look a node up by it.
	setClauseRE = regexp.MustCompile(`(?is)\bSET\b.*?(\bWHERE\b|$)`)
)

// notAnAlias holds the SQL keywords that can follow a table name where an
// alias could stand.
var notAnAlias = map[string]bool{
	"where": true, "set": true, "on": true, "left": true, "right": true, "full": true,
	"inner": true, "outer": true, "cross": true, "natural": true, "join": true, "order": true,
	"group": true, "limit": true, "having": true, "union": true, "indexed": true, "not": true,
	"using": true, "window": true, "returning": true, "except": true, "intersect": true,
	"as": true, "values": true, "and": true, "or": true, "select": true,
}

// tableRefRE returns the regexp that finds each place a statement reads or
// writes table, with the alias it gives it: FROM t, JOIN t a, UPDATE t,
// FROM x, t AS y.
func tableRefRE(table string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(?:\bFROM|\bJOIN|\bUPDATE|,)\s+` + regexp.QuoteMeta(table) +
		`\b(?:\s+(?:AS\s+)?([a-z_][a-z0-9_]*))?`)
}

// uidRefs returns the ways stmt can name the uid column of table: bare uid,
// <table>.uid, and <alias>.uid for each alias it gives table. A bare uid
// counts whenever the statement uses table, even beside another table with
// a uid column, so the guard errs toward asking for the term. None when
// stmt does not use table.
func uidRefs(stmt, table string) []string {
	matches := tableRefRE(table).FindAllStringSubmatch(stmt, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := map[string]bool{"uid": true, table + ".uid": true}
	for _, m := range matches {
		if alias := strings.ToLower(m[1]); alias != "" && !notAnAlias[alias] {
			seen[alias+".uid"] = true
		}
	}
	refs := make([]string, 0, len(seen))
	for ref := range seen {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	return refs
}

// refRE is the regexp source for ref as a whole column reference: not part
// of a longer name, and not qualified by another alias.
func refRE(ref string) string {
	return `(?:^|[^\w.])` + regexp.QuoteMeta(ref) + `\b`
}

// looksUpBy reports whether stmt compares ref for equality (=, ==, IN, or
// IS a value), on either side, with anything but the empty string: a lookup
// by uid. A search for the empty uid cannot use the index and is not one,
// nor is IS NULL or IS NOT. With boundOnly, only a comparison with a bound
// value (?) counts.
func looksUpBy(stmt, ref string, boundOnly bool) bool {
	counts := func(value string) bool {
		if boundOnly {
			return strings.HasPrefix(value, "?")
		}
		return value != "''"
	}
	left := regexp.MustCompile(`(?i)` + refRE(ref) + `\s*(?:==?|IN\b)\s*\(?\s*(\S{0,2})`)
	for _, m := range left.FindAllStringSubmatch(stmt, -1) {
		if counts(m[1]) {
			return true
		}
	}
	is := regexp.MustCompile(`(?i)` + refRE(ref) + `\s+IS\s+(\S{1,4})`)
	for _, m := range is.FindAllStringSubmatch(stmt, -1) {
		if v := strings.ToUpper(m[1]); !strings.HasPrefix(v, "NOT") && !strings.HasPrefix(v, "NULL") && counts(m[1]) {
			return true
		}
	}
	if boundOnly {
		return regexp.MustCompile(`\?\s*==?\s*` + regexp.QuoteMeta(ref) + `\b`).MatchString(stmt)
	}
	right := regexp.MustCompile(`(?i)(?:^|[^<>!=])==?\s*` + regexp.QuoteMeta(ref) + `\b`)
	return right.MatchString(stmt)
}

// carriesIndexTerm reports whether stmt carries the index term for ref:
// ref <> the empty string (written with <> or !=), the term that lets a
// lookup by ref use a partial uid index.
func carriesIndexTerm(stmt, ref string) bool {
	return regexp.MustCompile(`(?i)` + refRE(ref) + `\s*(?:<>|!=)\s*''`).MatchString(stmt)
}

// The guard's limits (review r1, MTIX-95.47). It reads text, not a parse
// tree, statement by statement:
//   - the term counts wherever it appears in the statement, not only in the
//     WHERE or ON clause of the lookup it serves (for a table used twice,
//     each alias needs its own term, so one alias's term does not cover
//     another's, but a bare uid term covers every bare lookup);
//   - it sees only SQL written as one Go literal (or literals joined by +)
//     in this package: a column or table spliced in at run time, schema-
//     qualified or quoted names ("nodes", main.nodes), and named parameters
//     (:uid, @uid, $uid) are not seen;
//   - it errs toward flagging: a bare uid counts as the table's whenever
//     the statement uses the table, even beside another table with a uid
//     column; for sync_events only a comparison with a bound value counts,
//     because the joins that compare its uid with the uid of nodes search
//     nodes, not sync_events.
// The EXPLAIN QUERY PLAN tests pin the plans of the known lookups; the guard
// is a tripwire for the next one, not a proof.

// classifyUIDLookups returns the references to the uid of table by which
// stmt looks table up, and those of them without the term that lets its
// partial uid index apply. Comments and the assignments of an UPDATE are
// not lookups.
func classifyUIDLookups(stmt string, table uidIndexedTable) (lookups, missing []string) {
	stmt = sqlCommentRE.ReplaceAllString(stmt, " ")
	refs := uidRefs(stmt, table.name)
	filters := setClauseRE.ReplaceAllString(stmt, " $1")
	for _, ref := range refs {
		if !looksUpBy(filters, ref, table.boundOnly) {
			continue
		}
		lookups = append(lookups, ref)
		if !carriesIndexTerm(filters, ref) {
			missing = append(missing, ref)
		}
	}
	return lookups, missing
}

// TestClassifyUIDLookups_Statements_FlagLookupsWithoutIndexTerm pins the
// guard's reading of SQL (MTIX-95.47): which statements look nodes up by
// uid, and which of those lack the index term (uid <> the empty string).
func TestClassifyUIDLookups_Statements_FlagLookupsWithoutIndexTerm(t *testing.T) {
	tests := []struct {
		name        string
		stmt        string
		wantLookups []string
		wantMissing []string
	}{
		{"bare lookup without the term", `SELECT id FROM nodes WHERE uid = ? AND deleted_at IS NULL`, []string{"uid"}, []string{"uid"}},
		{"bare lookup with the term", `SELECT id FROM nodes WHERE uid = ? AND uid <> '' AND deleted_at IS NULL`, []string{"uid"}, nil},
		{"the term written with !=", `SELECT id FROM nodes WHERE uid = ? AND uid != ''`, []string{"uid"}, nil},
		{"lower-case SQL", `select id from nodes where uid = ?`, []string{"uid"}, []string{"uid"}},
		{"IN list", `SELECT id FROM nodes WHERE uid IN (?, ?)`, []string{"uid"}, []string{"uid"}},
		{"IS a bound value", `SELECT id FROM nodes WHERE uid IS ?`, []string{"uid"}, []string{"uid"}},
		{"IS a bound value with the term", `SELECT id FROM nodes WHERE uid IS ? AND uid <> ''`, []string{"uid"}, nil},
		{"IS NOT NULL is not a lookup", `SELECT id FROM nodes WHERE uid IS NOT NULL`, nil, nil},
		{"IS NULL is not a lookup", `SELECT id FROM nodes WHERE uid IS NULL`, nil, nil},
		{"DELETE by uid", `DELETE FROM nodes WHERE uid = ?`, []string{"uid"}, []string{"uid"}},
		{"qualified by the table name", `SELECT id FROM nodes WHERE nodes.uid = ?`, []string{"nodes.uid"}, []string{"nodes.uid"}},
		{"alias with AS", `SELECT x.id FROM nodes AS x WHERE x.uid = ?`, []string{"x.uid"}, []string{"x.uid"}},
		{"join alias without the term", `SELECT e.event_id FROM sync_events e LEFT JOIN nodes n ON n.uid = e.uid`, []string{"n.uid"}, []string{"n.uid"}},
		{"join alias with the term", `SELECT e.event_id FROM sync_events e LEFT JOIN nodes n ON n.uid = e.uid AND n.uid <> ''`, []string{"n.uid"}, nil},
		{"alias on the right-hand side", `SELECT e.event_id FROM sync_events e JOIN nodes s ON e.event_id = s.uid`, []string{"s.uid"}, []string{"s.uid"}},
		{"one alias's term does not cover another", `SELECT 1 FROM sync_events e
			LEFT JOIN nodes n ON n.uid = e.uid AND n.uid <> ''
			LEFT JOIN nodes s ON s.uid = e.event_id`, []string{"n.uid", "s.uid"}, []string{"s.uid"}},
		{"a lookup in a subquery of an assignment", `UPDATE sync_events SET node_id = (SELECT id FROM nodes WHERE uid = ?) WHERE event_id = ?`, []string{"uid"}, []string{"uid"}},
		{"an UPDATE filtered by uid", `UPDATE nodes SET title = ? WHERE uid = ?`, []string{"uid"}, []string{"uid"}},
		{"an assignment of uid is not a lookup", `UPDATE nodes SET uid = ? WHERE id = ?`, nil, nil},
		{"a search for the empty uid is not a lookup", `SELECT id FROM nodes WHERE uid IS NULL OR uid = ''`, nil, nil},
		{"COALESCE of uid is not a lookup", `UPDATE nodes SET uid = ? WHERE id = ? AND COALESCE(uid, '') = ''`, nil, nil},
		{"another table's uid", `SELECT event_id FROM sync_events WHERE uid = ?`, nil, nil},
		{"another alias's uid beside nodes", `SELECT n.id FROM sync_events e JOIN nodes n ON n.id = e.node_id WHERE e.uid = ?`, nil, nil},
		{"uid in a comment", "-- WHERE uid = ? would scan\nSELECT id FROM nodes WHERE id = ?", nil, nil},
		{"prose, not SQL", `resolve display path for uid %s`, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookups, missing := classifyUIDLookups(tt.stmt, nodesUIDTable)
			assert.Equal(t, tt.wantLookups, lookups, "lookups")
			assert.Equal(t, tt.wantMissing, missing, "lookups without the term")
		})
	}
}

// TestClassifyUIDLookups_SyncEventsStatements_FlagBoundLookupsWithoutIndexTerm
// pins the guard's reading of SQL on sync_events (MTIX-95.47): a lookup by
// a bound uid needs the index term; a join that compares the uid of
// sync_events with the uid of nodes is not counted.
func TestClassifyUIDLookups_SyncEventsStatements_FlagBoundLookupsWithoutIndexTerm(t *testing.T) {
	tests := []struct {
		name        string
		stmt        string
		wantLookups []string
		wantMissing []string
	}{
		{"bound lookup without the term", `SELECT event_id FROM sync_events WHERE uid = ? AND op_type IN (?, ?)`, []string{"uid"}, []string{"uid"}},
		{"bound lookup with the term", `SELECT event_id FROM sync_events WHERE uid = ? AND uid <> '' AND op_type IN (?, ?)`, []string{"uid"}, nil},
		{"bound IN list", `SELECT event_id FROM sync_events WHERE uid IN (?, ?)`, []string{"uid"}, []string{"uid"}},
		{"IS a bound value", `SELECT event_id FROM sync_events WHERE uid IS ?`, []string{"uid"}, []string{"uid"}},
		{"bound value on the left-hand side", `SELECT e.event_id FROM sync_events e WHERE ? = e.uid`, []string{"e.uid"}, []string{"e.uid"}},
		{"a join with nodes is not counted", `SELECT e.event_id FROM sync_events e LEFT JOIN nodes n ON n.uid = e.uid AND n.uid <> ''`, nil, nil},
		{"an assignment of uid is not a lookup", `UPDATE sync_events SET uid = ? WHERE event_id = ?`, nil, nil},
		{"a search for events without a uid is not a lookup", `SELECT event_id FROM sync_events WHERE node_id = ? AND uid IS NULL`, nil, nil},
		{"the uid of nodes", `SELECT id FROM nodes WHERE uid = ?`, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookups, missing := classifyUIDLookups(tt.stmt, syncEventsUIDTable)
			assert.Equal(t, tt.wantLookups, lookups, "lookups")
			assert.Equal(t, tt.wantMissing, missing, "lookups without the term")
		})
	}
}

// TestPackageSQL_UIDLookups_CarryIndexTerm is the guard (MTIX-95.47): every
// lookup by uid of nodes, and by a bound uid of sync_events, in the
// package's production SQL carries the index term (uid <> the empty
// string), so the table's partial uid index applies. It also checks that it
// sees the known lookups, so a scan that finds nothing cannot pass for a
// clean one.
func TestPackageSQL_UIDLookups_CarryIndexTerm(t *testing.T) {
	tests := []struct {
		table uidIndexedTable
		known []string
	}{
		{nodesUIDTable, []string{
			"resolveNodeRef", "Store.ResolveDisplayPathByUID", "Store.loadSettleTarget",
			"idOfUID", "Store.PushSubjects", "Store.HeldPushEvents",
		}},
		{syncEventsUIDTable, []string{"heldWorkflowQuery", "readNodeEvents", "lwwPriorQuery"}},
	}
	lits := packageSQLLiterals(t)
	for _, tt := range tests {
		t.Run(tt.table.name, func(t *testing.T) {
			seen := map[string]bool{}
			var offenders []string
			for _, lit := range lits {
				lookups, missing := classifyUIDLookups(lit.query, tt.table)
				seen[lit.fn] = seen[lit.fn] || len(lookups) > 0
				for _, ref := range missing {
					offenders = append(offenders, fmt.Sprintf("%s (%s): looks %s up by %s without \"AND %s <> ''\"",
						lit.pos, lit.fn, tt.table.name, ref, ref))
				}
			}
			for _, fn := range tt.known {
				assert.True(t, seen[fn], "the guard must see the lookup of %s by uid in %s", tt.table.name, fn)
			}
			assert.Empty(t, offenders, "%s is partial (WHERE uid IS NOT NULL AND uid <> ''); SQLite uses it only "+
				"when the query also says uid <> '', so none of these can use it:\n%s",
				tt.table.index, strings.Join(offenders, "\n"))
		})
	}
}

// TestUIDIndexes_StoredDefinition_ArePartialOnNonEmptyUID pins the indexes
// the guard is written for (schema.go), as SQLite stores them:
//
//	CREATE UNIQUE INDEX idx_nodes_uid ON nodes(uid) WHERE uid IS NOT NULL AND uid <> ''
//	CREATE INDEX idx_sync_events_uid ON sync_events(uid) WHERE uid IS NOT NULL AND uid <> ''
//
// A change to that predicate changes the term every lookup needs, so it must
// fail here and the guard be updated with it; the fix itself changes no
// schema (MTIX-95.47).
func TestUIDIndexes_StoredDefinition_ArePartialOnNonEmptyUID(t *testing.T) {
	s := newInternalTestStore(t)
	for _, table := range []uidIndexedTable{nodesUIDTable, syncEventsUIDTable} {
		t.Run(table.index, func(t *testing.T) {
			var def string
			// The stored definition of the table's uid index.
			require.NoError(t, s.readDB.QueryRowContext(context.Background(),
				`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`, table.index).Scan(&def))
			kind := "CREATE INDEX " // nodes.uid is UNIQUE since MTIX-95.31.8
			if table.name == "nodes" {
				kind = "CREATE UNIQUE INDEX "
			}
			assert.Equal(t, kind+table.index+" ON "+table.name+"(uid) WHERE uid IS NOT NULL AND uid <> ''",
				strings.Join(strings.Fields(def), " "))
		})
	}
}
