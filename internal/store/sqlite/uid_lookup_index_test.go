// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// Lookups of a node by its uid use idx_nodes_uid (MTIX-95.47). Each test
// reads the query a function runs from the package's source (the guard's
// reader, uid_lookup_guard_test.go), asks SQLite for its plan on a real
// store, and checks the function still finds the same node.

// uidLookupIn returns the one SQL literal in function fn of the package's
// source that looks table up by uid: the query fn runs, read from its code.
func uidLookupIn(t *testing.T, lits []sqlLiteral, fn string, table uidIndexedTable) string {
	t.Helper()
	var found []string
	for _, l := range lits {
		if lookups, _ := classifyUIDLookups(l.query, table); l.fn == fn && len(lookups) > 0 {
			found = append(found, l.query)
		}
	}
	require.Len(t, found, 1, "%s must run exactly one lookup of %s by uid", fn, table.name)
	return found[0]
}

// queryPlan returns the detail lines of SQLite's plan for query on s, each
// parameter bound to a probe uid.
func queryPlan(t *testing.T, s *Store, query string) []string {
	t.Helper()
	args := make([]any, strings.Count(query, "?"))
	for i := range args {
		args[i] = "uid-probe"
	}
	// Built by concatenation under SQL Rule 1b (EXPLAIN of the package's own
	// statement in a test, approved by the maintainer for MTIX-95.47).
	// EXPLAIN QUERY PLAN takes a statement, not a value, so no bound
	// parameter can carry it. The text is one of the package's own SQL
	// statements, read from its source by uidLookupIn or returned by the
	// function that runs it; never input.
	rows, err := s.readDB.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var plan []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		plan = append(plan, detail)
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, plan, "SQLite must return a plan")
	return plan
}

// assertSearchesIndex asserts that plan reads the rows of table (a name or
// an alias) by the uid index index and never scans them or builds an
// automatic index over them.
func assertSearchesIndex(t *testing.T, plan []string, table, index string) {
	t.Helper()
	search := regexp.MustCompile(`^SEARCH ` + regexp.QuoteMeta(table) + ` USING (?:COVERING )?INDEX ` +
		regexp.QuoteMeta(index) + ` \(uid=\?\)`)
	full := regexp.MustCompile(`^SCAN ` + regexp.QuoteMeta(table) + `\b|^SEARCH ` + regexp.QuoteMeta(table) + ` USING AUTOMATIC`)
	var searched bool
	for _, line := range plan {
		searched = searched || search.MatchString(line)
		assert.False(t, full.MatchString(line), "reads every row of %s: %q in plan %q", table, line, plan)
	}
	assert.True(t, searched, "%s must be searched by %s; plan %q", table, index, plan)
}

// TestResolveNodeRef_EventWithUID_PlanSearchesIDXNodesUID: the query that
// resolves a pulled event's node by its uid, run once per pulled event
// that carries one, searches idx_nodes_uid instead of reading every node
// (MTIX-95.47).
func TestResolveNodeRef_EventWithUID_PlanSearchesIDXNodesUID(t *testing.T) {
	s := newInternalTestStore(t)
	query := uidLookupIn(t, packageSQLLiterals(t), "resolveNodeRef", nodesUIDTable)
	assertSearchesIndex(t, queryPlan(t, s, query), "nodes", "idx_nodes_uid")
}

// TestUIDLookupQueries_ExplainQueryPlan_SearchIDXNodesUID: every other
// lookup of a node by uid searches idx_nodes_uid (MTIX-95.47): the node-uid
// helper, settlement, the local renumber of an import, and the task of a
// pending or held push event, by uid and, for a creation queued without
// one, by its own event id.
func TestUIDLookupQueries_ExplainQueryPlan_SearchIDXNodesUID(t *testing.T) {
	tests := []struct {
		name  string
		fn    string
		table string
	}{
		{"node-uid helper", "Store.ResolveDisplayPathByUID", "nodes"},
		{"settlement", "Store.loadSettleTarget", "nodes"},
		{"local renumber of an import", "idOfUID", "nodes"},
		{"push subject by uid", "Store.PushSubjects", "n"},
		{"push subject of a creation without a uid, by its event id", "Store.PushSubjects", "s"},
		{"held push event by uid", "Store.HeldPushEvents", "n"},
		{"held push creation without a uid, by its event id", "Store.HeldPushEvents", "s"},
	}
	s := newInternalTestStore(t)
	lits := packageSQLLiterals(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSearchesIndex(t, queryPlan(t, s, uidLookupIn(t, lits, tt.fn, nodesUIDTable)), tt.table, "idx_nodes_uid")
		})
	}
}

// lwwQueryByUID returns the query detectLWWOutcome runs for a pulled event
// of op that carries a uid (lwwPriorQuery), checking it is scoped by the uid.
func lwwQueryByUID(t *testing.T, op model.OpType) string {
	t.Helper()
	e := &model.SyncEvent{UID: "uid-probe", NodeID: "PROJ-1", OpType: op, EventID: "e-probe"}
	query, args := lwwPriorQuery(e, "title")
	require.NotEmpty(t, args)
	require.Equal(t, "uid-probe", args[0], "the uid variant is scoped by the event's uid")
	return query
}

// TestSyncEventsUIDLookups_ExplainQueryPlan_SearchIDXSyncEventsUID: a lookup
// of sync events by uid searches the partial idx_sync_events_uid
// (MTIX-95.47): the workflow events already held for a node, walked for
// every pulled claim, unclaim, status change or defer that carries a uid;
// the prior event of a pulled field, prompt or acceptance change that
// carries a uid (last-writer-wins); and a node's events that sync repair
// --status reads.
func TestSyncEventsUIDLookups_ExplainQueryPlan_SearchIDXSyncEventsUID(t *testing.T) {
	s := newInternalTestStore(t)
	lits := packageSQLLiterals(t)
	tests := []struct {
		name  string
		query func(t *testing.T) string
	}{
		{"held workflow events by uid", func(t *testing.T) string {
			query, scope := heldWorkflowQuery(&model.SyncEvent{UID: "uid-probe", NodeID: "PROJ-1"})
			require.Equal(t, "uid-probe", scope, "the uid variant is scoped by the event's uid")
			return query
		}},
		{"node events by uid, for sync repair --status", func(t *testing.T) string {
			return uidLookupIn(t, lits, "readNodeEvents", syncEventsUIDTable)
		}},
		{"LWW prior of a pulled update_field by uid", func(t *testing.T) string {
			return lwwQueryByUID(t, model.OpUpdateField)
		}},
		{"LWW prior of a pulled set_prompt by uid", func(t *testing.T) string {
			return lwwQueryByUID(t, model.OpSetPrompt)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSearchesIndex(t, queryPlan(t, s, tt.query(t)), "sync_events", "idx_sync_events_uid")
		})
	}
}

// uidFixture is a store holding a live root PROJ-1 with a live child
// PROJ-1.1, a soft-deleted root PROJ-2, and a live root PROJ-3 whose uid is
// empty, as a node not yet backfilled holds it.
type uidFixture struct {
	s                       *Store
	live, child, deletedUID string
}

// newUIDFixture builds the uidFixture store.
func newUIDFixture(t *testing.T) uidFixture {
	t.Helper()
	ctx := context.Background()
	s := newInternalTestStore(t)
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	for _, n := range []*model.Node{
		makeTestNode("PROJ-1", "", "PROJ", "live", 0, 1, now),
		makeTestNode("PROJ-1.1", "PROJ-1", "PROJ", "child", 1, 1, now),
		makeTestNode("PROJ-2", "", "PROJ", "deleted", 0, 2, now),
		makeTestNode("PROJ-3", "", "PROJ", "no uid", 0, 3, now),
	} {
		require.NoError(t, s.CreateNode(ctx, n))
	}
	require.NoError(t, s.DeleteNode(ctx, "PROJ-2", false, "tester"))
	// Empty PROJ-3's uid, as a node the backfill has not reached holds it.
	_, err := s.writeDB.ExecContext(ctx, `UPDATE nodes SET uid = '' WHERE id = ?`, "PROJ-3")
	require.NoError(t, err)
	f := uidFixture{s: s, live: uidOf(t, s, "PROJ-1"), child: uidOf(t, s, "PROJ-1.1"), deletedUID: uidOf(t, s, "PROJ-2")}
	require.NotContains(t, []string{f.live, f.child, f.deletedUID}, "", "every fixture node but PROJ-3 holds a uid")
	// The hub knows every fixture task: an event that names one by number alone
	// is about it (a task whose creation is pending is not, MTIX-95.37).
	_, err = s.writeDB.ExecContext(ctx, `UPDATE sync_events SET sync_status = 'pushed'`)
	require.NoError(t, err)
	return f
}

// inTx runs f in a write transaction that is rolled back.
func inTx(t *testing.T, s *Store, f func(tx *sql.Tx) (string, error)) (string, error) {
	t.Helper()
	tx, err := s.writeDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	return f(tx)
}

// TestResolveNodeRef_EventKeying_ResolvesTheSameNode: resolveNodeRef finds
// the node it found before the index term (MTIX-95.47): by uid when the
// event carries one, whatever number the event names; by number when it
// carries none; never a soft-deleted node, and not found for an unknown uid.
func TestResolveNodeRef_EventKeying_ResolvesTheSameNode(t *testing.T) {
	f := newUIDFixture(t)
	require.NoError(t, f.s.CreateNode(context.Background(),
		makeTestNode("PROJ-4", "", "PROJ", "pending", 0, 4, time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC))))
	tests := []struct {
		name    string
		event   model.SyncEvent
		want    string
		wantErr error
	}{
		{"no uid: a task whose creation is pending", model.SyncEvent{NodeID: "PROJ-4"}, "", ErrNumberHeldByPendingCreate},
		{"uid of a live node, stale number", model.SyncEvent{UID: f.live, NodeID: "PROJ-9"}, "PROJ-1", nil},
		{"uid of a live child", model.SyncEvent{UID: f.child, NodeID: "PROJ-1"}, "PROJ-1.1", nil},
		{"uid of a soft-deleted node", model.SyncEvent{UID: f.deletedUID, NodeID: "PROJ-2"}, "", model.ErrNotFound},
		{"unknown uid, live number", model.SyncEvent{UID: "uid-unknown", NodeID: "PROJ-1"}, "", model.ErrNotFound},
		{"no uid: by number", model.SyncEvent{NodeID: "PROJ-1"}, "PROJ-1", nil},
		{"no uid: a node without a uid, by number", model.SyncEvent{NodeID: "PROJ-3"}, "PROJ-3", nil},
		{"no uid: a soft-deleted number", model.SyncEvent{NodeID: "PROJ-2"}, "", model.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := inTx(t, f.s, func(tx *sql.Tx) (string, error) {
				return resolveNodeRef(context.Background(), tx, &tt.event)
			})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestUIDLookupHelpers_ByUID_ResolveTheSameNode: the node-uid helper,
// settlement and the local renumber of an import find the node they found
// before the index term (MTIX-95.47): the helper and settlement only a
// live node, the renumber a live or soft-deleted one; an unknown uid is
// not found.
func TestUIDLookupHelpers_ByUID_ResolveTheSameNode(t *testing.T) {
	f := newUIDFixture(t)
	ctx := context.Background()
	helper := func(uid string) (string, error) { return f.s.ResolveDisplayPathByUID(ctx, uid) }
	settle := func(uid string) (string, error) {
		st, err := f.s.loadSettleTarget(ctx, uid)
		return st.id + " " + st.project + " " + st.parentID, err
	}
	renumber := func(uid string) (string, error) {
		return inTx(t, f.s, func(tx *sql.Tx) (string, error) { return idOfUID(ctx, tx, uid) })
	}
	tests := []struct {
		name    string
		resolve func(uid string) (string, error)
		uid     string
		want    string
		wantErr error
	}{
		{"helper: live", helper, f.live, "PROJ-1", nil},
		{"helper: live child", helper, f.child, "PROJ-1.1", nil},
		{"helper: soft-deleted", helper, f.deletedUID, "", model.ErrNotFound},
		{"helper: unknown", helper, "uid-unknown", "", model.ErrNotFound},
		{"helper: empty", helper, "", "", model.ErrNotFound},
		{"settle: live child", settle, f.child, "PROJ-1.1 PROJ PROJ-1", nil},
		{"settle: live root", settle, f.live, "PROJ-1 PROJ ", nil},
		{"settle: soft-deleted", settle, f.deletedUID, "", model.ErrNotFound},
		{"settle: unknown", settle, "uid-unknown", "", model.ErrNotFound},
		{"renumber: live", renumber, f.live, "PROJ-1", nil},
		{"renumber: soft-deleted", renumber, f.deletedUID, "PROJ-2", nil},
		{"renumber: unknown", renumber, "uid-unknown", "", model.ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.resolve(tt.uid)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestDetectLWWOutcome_ByUIDOrNumber_SameOutcome: detectLWWOutcome finds the
// same prior event, with the same outcome, whether the pulled event names
// its node by uid or, carrying none, by number (MTIX-95.47): the uid
// variant's index term and planner hint change the plan, not the result.
func TestDetectLWWOutcome_ByUIDOrNumber_SameOutcome(t *testing.T) {
	f := newUIDFixture(t)
	ctx := context.Background()
	for _, change := range []string{"first", "second"} {
		title, prompt := "title "+change, "prompt "+change
		require.NoError(t, f.s.UpdateNode(ctx, "PROJ-1", &store.NodeUpdate{Title: &title, Prompt: &prompt}))
	}
	titleChange := json.RawMessage(`{"field_name":"title","new_value":"pulled"}`)
	promptChange := json.RawMessage(`{"prompt_text":"pulled"}`)
	tests := []struct {
		name    string
		op      model.OpType
		payload json.RawMessage
		lamport int64
		wantWin bool
	}{
		{"update_field, older", model.OpUpdateField, titleChange, 0, false},
		{"update_field, newer", model.OpUpdateField, titleChange, 1 << 40, true},
		{"set_prompt, older", model.OpSetPrompt, promptChange, 0, false},
		{"set_prompt, newer", model.OpSetPrompt, promptChange, 1 << 40, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []lwwOutcome
			for _, uid := range []string{f.live, ""} {
				e := &model.SyncEvent{EventID: "e-pulled", NodeID: "PROJ-1", UID: uid, OpType: tt.op,
					Payload: tt.payload, LamportClock: tt.lamport, AuthorMachineHash: "m"}
				tx, err := f.s.writeDB.BeginTx(ctx, nil)
				require.NoError(t, err)
				out, err := detectLWWOutcome(ctx, tx, e)
				require.NoError(t, tx.Rollback())
				require.NoError(t, err)
				got = append(got, out)
			}
			require.True(t, got[0].HasPrior, "the local changes are priors")
			assert.NotEmpty(t, got[0].PriorEventID)
			assert.Equal(t, tt.wantWin, got[0].IncomingWins)
			assert.Equal(t, got[0], got[1], "by uid and by number find the same prior and outcome")
		})
	}
}
