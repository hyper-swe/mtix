// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-95.21 regressions (review F-87, ADR-006 D53): a cascade cancel ran
// the dependent unblock and the progress recompute for the root alone, so
// a dependent of a cancelled descendant stayed blocked and the progress of
// the root and of every cancelled intermediate node went stale. A cascade
// must leave the store as a single-node cancel of each node it cancels
// would, in one transaction. The cascade still emits no sync event for the
// descendants in 0.5.x (ADR-006 D10); those tests pin what replicas see.
package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// cascadeTestTime is the fixed creation time of every fixture node.
var cascadeTestTime = time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

// treeDepth is a GetTree depth that covers every fixture subtree.
const treeDepth = 10

// Fixture ids shared by the dependent tests: a root with a child, a
// grandchild under that child and a second child, plus two nodes outside
// the subtree.
const (
	ccRoot       = "PROJ-1"
	ccChild      = "PROJ-1.1"
	ccGrandchild = "PROJ-1.1.1"
	ccChild2     = "PROJ-1.2"
	ccOutside    = "PROJ-2" // the dependent under test
	ccOutsider   = "PROJ-3" // a blocker outside the cancelled subtree
)

// ccNode builds a fixture node with the given id and status. The parent,
// depth and sequence follow from the dot-notation id.
func ccNode(id string, status model.Status) *model.Node {
	parent := model.ParseIDParent(id)
	n := makeChildNode(id, parent, "PROJ", "node "+id, model.ParseIDDepth(id), seqOf(id), cascadeTestTime)
	if parent == "" {
		n = makeRootNode(id, "PROJ", "node "+id, cascadeTestTime)
		n.Seq = seqOf(id)
	}
	n.Status = status
	if status == model.StatusDone {
		n.Progress = 1.0
	}
	if status == model.StatusInProgress {
		n.Assignee = "agent-1"
	}
	return n
}

// seqOf returns the last numeric segment of a dot-notation id.
func seqOf(id string) int {
	seq, err := strconv.Atoi(id[strings.LastIndexAny(id, ".-")+1:])
	if err != nil {
		panic("fixture id without a numeric last segment: " + id)
	}
	return seq
}

// topOf returns the top-level ancestor of a dot-notation id.
func topOf(id string) string {
	if i := strings.Index(id, "."); i >= 0 {
		return id[:i]
	}
	return id
}

// seedCascadeNodes creates nodes in order in s.
func seedCascadeNodes(t *testing.T, s *sqlite.Store, nodes ...*model.Node) {
	t.Helper()
	for _, n := range nodes {
		require.NoError(t, s.CreateNode(context.Background(), n), "seed %s", n.ID)
	}
}

// markDone moves an open node to done through the state machine, which sets
// its progress to 1.0 whatever its children are.
func markDone(t *testing.T, s *sqlite.Store, id string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.TransitionStatus(ctx, id, model.StatusInProgress, "start", "test"))
	require.NoError(t, s.TransitionStatus(ctx, id, model.StatusDone, "finished", "test"))
}

// statusOf returns the status of a live node.
func statusOf(t *testing.T, s *sqlite.Store, id string) model.Status {
	t.Helper()
	n, err := s.GetNode(context.Background(), id)
	require.NoError(t, err, "read %s", id)
	return n.Status
}

// recordProgressWrites installs a trigger that logs, in statement order,
// every UPDATE that sets nodes.progress. The returned func reads the log.
func recordProgressWrites(t *testing.T, s *sqlite.Store) func() []string {
	t.Helper()
	// Test-only log table and trigger (constant DDL).
	_, err := s.WriteDB().Exec(`CREATE TABLE test_progress_writes (
		seq INTEGER PRIMARY KEY AUTOINCREMENT, node_id TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = s.WriteDB().Exec(`CREATE TRIGGER test_log_progress_writes
		AFTER UPDATE OF progress ON nodes
		BEGIN INSERT INTO test_progress_writes (node_id) VALUES (NEW.id); END`)
	require.NoError(t, err)
	return func() []string {
		// Read the log in write order.
		rows, qErr := s.Query(context.Background(),
			`SELECT node_id FROM test_progress_writes ORDER BY seq`)
		require.NoError(t, qErr)
		defer func() { _ = rows.Close() }()
		var ids []string
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			ids = append(ids, id)
		}
		require.NoError(t, rows.Err())
		return ids
	}
}

// transitionEventsSince returns, per node, the transition_status payloads
// emitted after lamport clock `after`.
func transitionEventsSince(t *testing.T, s *sqlite.Store, after int64) map[string][]model.TransitionStatusPayload {
	t.Helper()
	// Every transition_status event newer than the baseline clock.
	rows, err := s.Query(context.Background(),
		`SELECT node_id, payload FROM sync_events
		 WHERE op_type = ? AND lamport_clock > ? ORDER BY lamport_clock`,
		string(model.OpTransitionStatus), after)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	out := map[string][]model.TransitionStatusPayload{}
	for rows.Next() {
		var nodeID, payload string
		require.NoError(t, rows.Scan(&nodeID, &payload))
		var p model.TransitionStatusPayload
		require.NoError(t, json.Unmarshal([]byte(payload), &p))
		out[nodeID] = append(out[nodeID], p)
	}
	require.NoError(t, rows.Err())
	return out
}

// maxLamport returns the highest lamport clock in sync_events (0 if none).
func maxLamport(t *testing.T, s *sqlite.Store) int64 {
	t.Helper()
	var v sql.NullInt64
	// Baseline clock before the mutation under test.
	require.NoError(t, s.QueryRow(context.Background(),
		`SELECT MAX(lamport_clock) FROM sync_events`).Scan(&v))
	return v.Int64
}

// TestCascadeCancel_UnblocksDependentsOfDescendants: every descendant the
// cascade cancels resolves the `blocks` dependencies it is the blocker of,
// exactly as a single-node cancel of it would (FR-3.8). Before MTIX-95.21
// only the root's dependents were unblocked.
func TestCascadeCancel_UnblocksDependentsOfDescendants(t *testing.T) {
	type dep struct{ from, to string }
	tests := []struct {
		name          string
		outsideStatus model.Status
		deps          []dep
		want          map[string]model.Status
	}{
		{"dependent of a child is restored to open", model.StatusOpen,
			[]dep{{ccChild2, ccOutside}},
			map[string]model.Status{ccOutside: model.StatusOpen}},
		{"dependent of a grandchild is restored to its previous in_progress", model.StatusInProgress,
			[]dep{{ccGrandchild, ccOutside}},
			map[string]model.Status{ccOutside: model.StatusInProgress}},
		{"dependent of two descendants is restored once both are cancelled", model.StatusOpen,
			[]dep{{ccChild, ccOutside}, {ccChild2, ccOutside}},
			map[string]model.Status{ccOutside: model.StatusOpen}},
		{"dependent with an open blocker outside the subtree stays blocked", model.StatusOpen,
			[]dep{{ccChild, ccOutside}, {ccOutsider, ccOutside}},
			map[string]model.Status{ccOutside: model.StatusBlocked}},
		{"blocked descendant stays cancelled, not restored", model.StatusOpen,
			[]dep{{ccChild, ccChild2}},
			map[string]model.Status{ccChild2: model.StatusCancelled, ccOutside: model.StatusOpen}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			seedCascadeNodes(t, s,
				ccNode(ccRoot, model.StatusOpen), ccNode(ccChild, model.StatusOpen),
				ccNode(ccGrandchild, model.StatusOpen), ccNode(ccChild2, model.StatusOpen),
				ccNode(ccOutside, tt.outsideStatus), ccNode(ccOutsider, model.StatusOpen))
			for _, d := range tt.deps {
				addBlocker(ctx, t, s, d.from, d.to, cascadeTestTime)
			}

			require.NoError(t, s.CancelNode(ctx, ccRoot, "feature dropped", "pm-1", true))

			for _, id := range []string{ccRoot, ccChild, ccGrandchild} {
				assert.Equal(t, model.StatusCancelled, statusOf(t, s, id), "%s cancelled by the cascade", id)
			}
			for id, want := range tt.want {
				assert.Equal(t, want, statusOf(t, s, id), "status of %s", id)
			}
		})
	}
}

// progressCase is one fixture for the cascade progress tests: setup seeds
// it, root is the node cancelled with cascade, and want lists the progress
// every listed node must end with.
type progressCase struct {
	name  string
	setup func(t *testing.T, s *sqlite.Store)
	root  string
	want  map[string]float64
}

// cascadeProgressCases are the stale values a root-only recompute leaves:
// the root itself, a cancelled intermediate node, and an ancestor above a
// done intermediate node whose own progress the cascade changes.
func cascadeProgressCases() []progressCase {
	return []progressCase{
		{
			// PROJ-1.1 is 0.5 (one done child of two); cancelling the open
			// child leaves only the done one, so it must become 1.0.
			name: "root keeps a done child",
			setup: func(t *testing.T, s *sqlite.Store) {
				seedCascadeNodes(t, s, ccNode("PROJ-1", model.StatusOpen),
					ccNode("PROJ-1.1", model.StatusOpen), ccNode("PROJ-1.1.1", model.StatusDone),
					ccNode("PROJ-1.1.2", model.StatusOpen), ccNode("PROJ-1.2", model.StatusOpen))
			},
			root: "PROJ-1.1",
			want: map[string]float64{"PROJ-1.1": 1.0, "PROJ-1": 0.0},
		},
		{
			// PROJ-1.1 is 0.5 and PROJ-1 is 0.75 before the cascade.
			name: "cancelled intermediate keeps a done child",
			setup: func(t *testing.T, s *sqlite.Store) {
				seedCascadeNodes(t, s, ccNode("PROJ-1", model.StatusOpen),
					ccNode("PROJ-1.1", model.StatusOpen), ccNode("PROJ-1.1.1", model.StatusDone),
					ccNode("PROJ-1.1.2", model.StatusOpen), ccNode("PROJ-1.2", model.StatusDone))
			},
			root: "PROJ-1",
			want: map[string]float64{"PROJ-1.1": 1.0, "PROJ-1": 1.0},
		},
		{
			// PROJ-1.1 is done (1.0) over one open child; once that child is
			// cancelled it has no counted child left (FR-5.6b: 0.0), which
			// changes the root it rolls up into.
			name: "ancestor above a done intermediate",
			setup: func(t *testing.T, s *sqlite.Store) {
				seedCascadeNodes(t, s, ccNode("PROJ-1", model.StatusOpen),
					ccNode("PROJ-1.1", model.StatusOpen), ccNode("PROJ-1.1.1", model.StatusOpen),
					ccNode("PROJ-1.2", model.StatusDone))
				markDone(t, s, "PROJ-1.1")
			},
			root: "PROJ-1",
			want: map[string]float64{"PROJ-1.1": 0.0, "PROJ-1": 0.5},
		},
	}
}

// TestCascadeCancel_RecomputesAncestorProgress: after a cascade the progress
// of the root, of every cancelled intermediate node and of every ancestor
// above them is recomputed from the new child statuses (FR-5.4, FR-5.7).
// Before MTIX-95.21 only the root's parent chain was recomputed.
func TestCascadeCancel_RecomputesAncestorProgress(t *testing.T) {
	for _, tt := range cascadeProgressCases() {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			tt.setup(t, s)

			require.NoError(t, s.CancelNode(context.Background(), tt.root, "descoped", "pm-1", true))

			for id, want := range tt.want {
				assert.InDelta(t, want, progressOf(t, s, id), 1e-9, "progress of %s", id)
			}
		})
	}
}

// TestCascadeCancel_Progress_MatchesSingleNodeCancels: the cascade leaves
// every node with the progress that single-node cancels of the same nodes,
// deepest first and the root last, produce.
func TestCascadeCancel_Progress_MatchesSingleNodeCancels(t *testing.T) {
	for _, tt := range cascadeProgressCases() {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			cascaded := newTestStore(t)
			tt.setup(t, cascaded)
			oneByOne := newTestStore(t)
			tt.setup(t, oneByOne)

			// Nodes the cascade will cancel: the root's non-terminal
			// descendants, deepest first (GetTree order reversed).
			tree, err := oneByOne.GetTree(ctx, tt.root, treeDepth)
			require.NoError(t, err)
			for i := len(tree) - 1; i >= 0; i-- {
				n := tree[i]
				if n.ID == tt.root || n.Status == model.StatusDone ||
					n.Status == model.StatusCancelled || n.Status == model.StatusInvalidated {
					continue
				}
				require.NoError(t, oneByOne.CancelNode(ctx, n.ID, "descoped", "pm-1", false))
			}
			require.NoError(t, oneByOne.CancelNode(ctx, tt.root, "descoped", "pm-1", false))

			require.NoError(t, cascaded.CancelNode(ctx, tt.root, "descoped", "pm-1", true))

			all, err := oneByOne.GetTree(ctx, topOf(tt.root), treeDepth)
			require.NoError(t, err)
			for _, n := range all {
				assert.InDelta(t, n.Progress, progressOf(t, cascaded, n.ID), 1e-9, "progress of %s", n.ID)
				assert.Equal(t, n.Status, statusOf(t, cascaded, n.ID), "status of %s", n.ID)
			}
		})
	}
}

// TestCascadeCancel_RecomputesEachAncestorOnceDeepestFirst: every ancestor
// of a cancelled descendant, inside the subtree and above the root, is
// recomputed exactly once, each after all of its affected children, and the
// cancelled leaves are not recomputed at all.
func TestCascadeCancel_RecomputesEachAncestorOnceDeepestFirst(t *testing.T) {
	s := newTestStore(t)
	seedCascadeNodes(t, s,
		ccNode("PROJ-1", model.StatusOpen), ccNode("PROJ-1.1", model.StatusOpen),
		ccNode("PROJ-1.1.1", model.StatusOpen), ccNode("PROJ-1.1.1.1", model.StatusOpen),
		ccNode("PROJ-1.1.1.2", model.StatusOpen),
		ccNode("PROJ-1.1.2", model.StatusOpen), ccNode("PROJ-1.1.2.1", model.StatusOpen),
		ccNode("PROJ-1.1.3", model.StatusOpen), ccNode("PROJ-1.1.3.1", model.StatusOpen))
	markDone(t, s, "PROJ-1.1.3") // a done intermediate above an open leaf
	writes := recordProgressWrites(t, s)

	require.NoError(t, s.CancelNode(context.Background(), "PROJ-1.1", "descoped", "pm-1", true))

	log := writes()
	require.ElementsMatch(t,
		[]string{"PROJ-1.1.1", "PROJ-1.1.2", "PROJ-1.1.3", "PROJ-1.1", "PROJ-1"}, log,
		"each ancestor is recomputed exactly once and no cancelled leaf is")
	pos := map[string]int{}
	for i, id := range log {
		pos[id] = i
	}
	for _, child := range []string{"PROJ-1.1.1", "PROJ-1.1.2", "PROJ-1.1.3"} {
		assert.Less(t, pos[child], pos["PROJ-1.1"], "%s is recomputed before the root", child)
	}
	assert.Less(t, pos["PROJ-1.1"], pos["PROJ-1"], "the root is recomputed before its parent")
}

// TestCascadeCancel_FailureMidCascade_RollsBackWholeCancel: the descendant
// update, the unblock and the progress recompute of the descendants run in
// the cancel's own transaction, so a failure in any of them leaves nothing
// cancelled.
func TestCascadeCancel_FailureMidCascade_RollsBackWholeCancel(t *testing.T) {
	tests := []struct {
		name    string
		trigger string // constant test DDL that aborts one cascade step
	}{
		{"cancelling a descendant fails", `CREATE TRIGGER test_fail_step
			BEFORE UPDATE OF status ON nodes
			WHEN OLD.id = 'PROJ-1.1' AND NEW.status = 'cancelled'
			BEGIN SELECT RAISE(ABORT, 'injected failure'); END`},
		{"unblock of a descendant's dependent fails", `CREATE TRIGGER test_fail_step
			BEFORE UPDATE OF status ON nodes
			WHEN OLD.id = 'PROJ-2' AND OLD.status = 'blocked'
			BEGIN SELECT RAISE(ABORT, 'injected failure'); END`},
		{"progress of a cancelled intermediate fails", `CREATE TRIGGER test_fail_step
			BEFORE UPDATE OF progress ON nodes
			WHEN OLD.id = 'PROJ-1.1'
			BEGIN SELECT RAISE(ABORT, 'injected failure'); END`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			seedCascadeNodes(t, s,
				ccNode(ccRoot, model.StatusOpen), ccNode(ccChild, model.StatusOpen),
				ccNode(ccGrandchild, model.StatusOpen), ccNode(ccChild2, model.StatusOpen),
				ccNode(ccOutside, model.StatusOpen))
			addBlocker(ctx, t, s, ccChild2, ccOutside, cascadeTestTime)
			_, err := s.WriteDB().Exec(tt.trigger)
			require.NoError(t, err)

			err = s.CancelNode(ctx, ccRoot, "feature dropped", "pm-1", true)
			require.Error(t, err, "the injected failure must fail the cancel")
			assert.Contains(t, err.Error(), "injected failure")

			for _, id := range []string{ccRoot, ccChild, ccGrandchild, ccChild2} {
				assert.Equal(t, model.StatusOpen, statusOf(t, s, id), "%s rolled back", id)
			}
			assert.Equal(t, model.StatusBlocked, statusOf(t, s, ccOutside), "dependent rolled back")
		})
	}
}

// TestCascadeCancel_SoftDeletedDescendant_LeftUntouched: the cascade skips a
// soft-deleted descendant (deleted_at IS NULL guard). It is not cancelled,
// so its dependent is not released and its parent is not recomputed on its
// account, and undeleting it brings it back with the status it had.
func TestCascadeCancel_SoftDeletedDescendant_LeftUntouched(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedCascadeNodes(t, s,
		ccNode(ccRoot, model.StatusOpen), ccNode(ccChild, model.StatusOpen),
		ccNode(ccGrandchild, model.StatusOpen), ccNode(ccChild2, model.StatusOpen),
		ccNode(ccOutside, model.StatusOpen))
	addBlocker(ctx, t, s, ccGrandchild, ccOutside, cascadeTestTime)
	require.NoError(t, s.DeleteNode(ctx, ccGrandchild, false, "pm-1"))
	writes := recordProgressWrites(t, s)

	require.NoError(t, s.CancelNode(ctx, ccRoot, "feature dropped", "pm-1", true))

	assert.Equal(t, []string{ccRoot}, writes(),
		"only the root is recomputed: the deleted grandchild's parent has no cancelled child")
	assert.Equal(t, model.StatusBlocked, statusOf(t, s, ccOutside),
		"the deleted grandchild's dependent is not released by the cascade")
	assert.Equal(t, model.StatusCancelled, statusOf(t, s, ccChild))
	assert.Equal(t, model.StatusCancelled, statusOf(t, s, ccChild2))

	require.NoError(t, s.UndeleteNode(ctx, ccGrandchild))
	got, err := s.GetNode(ctx, ccGrandchild)
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status, "the deleted grandchild keeps its status")
	assert.Nil(t, got.ClosedAt, "the deleted grandchild was never closed")
}

// TestCascadeCancel_WithDescendants_EmitsNoPerDescendantEvents pins the
// 0.5.x sync behaviour the docs describe (ADR-006 D10, OD-2 for v2): the
// cascade emits one transition_status for the root and one for each
// dependent it unblocks, and none for the descendants it cancels.
func TestCascadeCancel_WithDescendants_EmitsNoPerDescendantEvents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedCascadeNodes(t, s,
		ccNode(ccRoot, model.StatusOpen), ccNode(ccChild, model.StatusOpen),
		ccNode(ccGrandchild, model.StatusOpen), ccNode(ccOutside, model.StatusOpen))
	addBlocker(ctx, t, s, ccGrandchild, ccOutside, cascadeTestTime)
	baseline := maxLamport(t, s)

	require.NoError(t, s.CancelNode(ctx, ccRoot, "feature dropped", "pm-1", true))

	events := transitionEventsSince(t, s, baseline)
	require.Len(t, events[ccRoot], 1, "one event for the root")
	assert.Equal(t, model.StatusCancelled, events[ccRoot][0].To)
	require.Len(t, events[ccOutside], 1, "one event for the unblocked dependent")
	assert.Equal(t, model.StatusBlocked, events[ccOutside][0].From)
	assert.Equal(t, model.StatusOpen, events[ccOutside][0].To)
	assert.Empty(t, events[ccChild], "no event for a cancelled descendant")
	assert.Empty(t, events[ccGrandchild], "no event for a cancelled descendant")
	assert.Len(t, events, 2, "no other transition_status event")
}

// TestCascadeCancel_PulledByReplica_DescendantsKeepTheirStatus pins what
// another replica sees of a cascade in 0.5.x: the root's cancel and the
// dependent's unblock arrive as events, the descendants' cancellation does
// not, so there the descendant stays open while the dependent it blocked is
// unblocked.
func TestCascadeCancel_PulledByReplica_DescendantsKeepTheirStatus(t *testing.T) {
	ctx := context.Background()
	origin, rawOrigin := mutationTestStore(t)
	replica, _ := mutationTestStore(t)
	mustCreateNode(t, origin, "MTIX-1", "")
	mustCreateNode(t, origin, "MTIX-1.1", "MTIX-1")
	mustCreateNode(t, origin, "MTIX-2", "")
	require.NoError(t, origin.AddDependency(ctx, &model.Dependency{
		FromID: "MTIX-1.1", ToID: "MTIX-2", DepType: model.DepTypeBlocks,
		CreatedAt: cascadeTestTime, CreatedBy: "pm-1",
	}))
	pullEvents(t, replica, pushPendingOwnEvents(t, rawOrigin))
	require.Equal(t, model.StatusBlocked, statusOf(t, replica, "MTIX-2"), "precondition")

	require.NoError(t, origin.CancelNode(ctx, "MTIX-1", "feature dropped", "pm-1", true))
	pullEvents(t, replica, pushPendingOwnEvents(t, rawOrigin))

	assert.Equal(t, model.StatusCancelled, statusOf(t, replica, "MTIX-1"), "the root's cancel is synced")
	assert.Equal(t, model.StatusOpen, statusOf(t, replica, "MTIX-1.1"),
		"no event for the descendant: the replica keeps it open")
	assert.Equal(t, model.StatusOpen, statusOf(t, replica, "MTIX-2"),
		"the dependent's unblock is synced as its own event")
}

// TestCancelNode_WithoutCascade_KeepsSingleNodeBehaviour guards the
// cascade=false path: it cancels the node alone, emits one transition_status
// for it, recomputes its parent chain (not the node itself) and unblocks its
// own dependents only.
func TestCancelNode_WithoutCascade_KeepsSingleNodeBehaviour(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedCascadeNodes(t, s,
		ccNode("PROJ-1", model.StatusOpen), ccNode("PROJ-1.1", model.StatusOpen),
		ccNode("PROJ-1.1.1", model.StatusDone), ccNode("PROJ-1.1.2", model.StatusOpen),
		ccNode("PROJ-1.2", model.StatusDone),
		ccNode("PROJ-2", model.StatusOpen), ccNode("PROJ-3", model.StatusOpen))
	addBlocker(ctx, t, s, "PROJ-1.1", "PROJ-2", cascadeTestTime)   // the node's dependent
	addBlocker(ctx, t, s, "PROJ-1.1.2", "PROJ-3", cascadeTestTime) // a child's dependent
	require.InDelta(t, 0.5, progressOf(t, s, "PROJ-1.1"), 1e-9, "precondition")
	baseline := maxLamport(t, s)
	writes := recordProgressWrites(t, s)

	require.NoError(t, s.CancelNode(ctx, "PROJ-1.1", "descoped", "pm-1", false))

	assert.Equal(t, model.StatusCancelled, statusOf(t, s, "PROJ-1.1"))
	assert.Equal(t, model.StatusOpen, statusOf(t, s, "PROJ-1.1.2"), "children untouched")
	assert.Equal(t, model.StatusDone, statusOf(t, s, "PROJ-1.1.1"), "children untouched")
	assert.Equal(t, model.StatusOpen, statusOf(t, s, "PROJ-2"), "own dependent unblocked")
	assert.Equal(t, model.StatusBlocked, statusOf(t, s, "PROJ-3"), "a child's dependent stays blocked")
	assert.InDelta(t, 0.5, progressOf(t, s, "PROJ-1.1"), 1e-9, "the node's own progress is kept")
	assert.InDelta(t, 1.0, progressOf(t, s, "PROJ-1"), 1e-9, "the parent excludes the cancelled node")
	assert.Equal(t, []string{"PROJ-1"}, writes(), "only the parent chain is recomputed")

	events := transitionEventsSince(t, s, baseline)
	require.Len(t, events["PROJ-1.1"], 1, "one event for the cancelled node")
	assert.Equal(t, model.StatusCancelled, events["PROJ-1.1"][0].To)
	require.Len(t, events["PROJ-2"], 1, "one event for its unblocked dependent")
	assert.Len(t, events, 2, "no other transition_status event")
}
