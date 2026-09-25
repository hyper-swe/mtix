// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// Applying a pulled or cloned create_node advances the local sequence
// counter of the node's parent key, in the apply's transaction
// (MTIX-95.38). Before, apply inserted the node and left the counter where
// it was, so the next local create under that parent could pick the pulled
// node's number and fail with "already exists".

// errRollBack makes a WithTx callback roll its transaction back.
var errRollBack = errors.New("roll back")

// sequenceCounter returns the value of the sequence counter key on raw,
// 0 when the key has no row.
func sequenceCounter(t *testing.T, raw *sql.DB, key string) int {
	t.Helper()
	var v int
	err := raw.QueryRow(`SELECT value FROM sequences WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	require.NoError(t, err)
	return v
}

// createLocally creates a PROJ task under parentID as a local create does:
// it takes the number from the parent's counter (NextSequence), then
// inserts the node. It returns the id and the insert's error.
func createLocally(t *testing.T, s *Store, parentID, title string) (string, error) {
	t.Helper()
	ctx := context.Background()
	seq, err := s.NextSequence(ctx, sequenceKey("PROJ", parentID))
	require.NoError(t, err)
	depth := computeDepth(parentID)
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	n := &model.Node{
		ID: model.BuildID("PROJ", parentID, seq), ParentID: parentID, Project: "PROJ",
		Depth: depth, Seq: seq, Title: title, NodeType: model.NodeTypeForDepth(depth),
		Priority: model.PriorityMedium, Status: model.StatusOpen, Weight: 1.0,
		Creator: "local", CreatedAt: now, UpdatedAt: now,
	}
	n.ContentHash = n.ComputeHash()
	return n.ID, s.CreateNode(ctx, n)
}

// pulledCreate builds a teammate's create_node for the PROJ task id, whose
// parent is the id's parent.
func pulledCreate(t *testing.T, id string, lamport int64) *model.SyncEvent {
	t.Helper()
	e := makeApplyEvent(t, model.OpCreateNode, id, "teammate", lamport,
		&model.CreateNodePayload{Title: "pulled " + id, ParentID: model.ParseIDParent(id)})
	e.ProjectPrefix = "PROJ"
	return e
}

// TestIdempotentApply_PulledCreates_NextLocalCreateUnderEachParentSucceeds:
// a store that created PROJ-1 and PROJ-2 pulls a teammate's PROJ-3, creates
// PROJ-3.1 itself, then pulls the teammate's PROJ-3.2. Each apply advances
// the counter of the pulled node's parent key to the pulled number, so a
// local create under each parent succeeds at the first attempt with the
// next free number: PROJ-4 and PROJ-3.3. Before MTIX-95.38 the counters
// stayed at 2 and 1, and both creates failed with ErrAlreadyExists.
func TestIdempotentApply_PulledCreates_NextLocalCreateUnderEachParentSucceeds(t *testing.T) {
	s, raw := applyTestStore(t)
	for _, title := range []string{"local one", "local two"} {
		_, err := createLocally(t, s, "", title)
		require.NoError(t, err)
	}
	require.NoError(t, applyOnce(t, s, pulledCreate(t, "PROJ-3", 10)))
	require.Equal(t, 3, sequenceCounter(t, raw, "PROJ:"), "the root counter reaches the pulled PROJ-3")
	local, err := createLocally(t, s, "PROJ-3", "local child")
	require.NoError(t, err)
	require.Equal(t, "PROJ-3.1", local)
	require.NoError(t, applyOnce(t, s, pulledCreate(t, "PROJ-3.2", 11)))
	require.Equal(t, 2, sequenceCounter(t, raw, "PROJ:PROJ-3"), "PROJ-3's counter reaches the pulled PROJ-3.2")

	root, err := createLocally(t, s, "", "next root")
	require.NoError(t, err, "the first attempt succeeds")
	require.Equal(t, "PROJ-4", root)
	child, err := createLocally(t, s, "PROJ-3", "next child")
	require.NoError(t, err, "the first attempt succeeds")
	require.Equal(t, "PROJ-3.3", child)
}

// TestIdempotentApply_CreateNode_CounterWrittenInTheApplyTransaction: the
// counter the apply advances is visible inside the apply's transaction and
// is rolled back with it, together with the node.
func TestIdempotentApply_CreateNode_CounterWrittenInTheApplyTransaction(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	var inTx int

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		if applyErr := IdempotentApply(ctx, tx, pulledCreate(t, "PROJ-3", 10)); applyErr != nil {
			return applyErr
		}
		if scanErr := tx.QueryRowContext(ctx,
			`SELECT value FROM sequences WHERE key = 'PROJ:'`).Scan(&inTx); scanErr != nil {
			return scanErr
		}
		return errRollBack
	})

	require.ErrorIs(t, err, errRollBack)
	require.Equal(t, 3, inTx, "the apply's transaction holds the advanced counter")
	require.Zero(t, sequenceCounter(t, raw, "PROJ:"), "the rollback takes the counter back")
	require.Zero(t, countNodes(t, raw), "and the node")
}

// TestIdempotentApply_CreateNode_CounterNeverLowered: a re-apply of the
// same create_node leaves the counter unchanged, and a pulled number below
// the counter keeps the counter's value.
func TestIdempotentApply_CreateNode_CounterNeverLowered(t *testing.T) {
	tests := []struct {
		name  string
		seed  int
		apply func(t *testing.T, s *Store)
		want  int
	}{
		{"re-apply after a local create moved the counter on", 0, func(t *testing.T, s *Store) {
			e := pulledCreate(t, "PROJ-3", 10)
			require.NoError(t, applyOnce(t, s, e))
			id, err := createLocally(t, s, "", "local")
			require.NoError(t, err)
			require.Equal(t, "PROJ-4", id)
			require.NoError(t, applyOnce(t, s, e))
		}, 4},
		{"pulled number below the counter", 10, func(t *testing.T, s *Store) {
			require.NoError(t, applyOnce(t, s, pulledCreate(t, "PROJ-3", 10)))
		}, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := applyTestStore(t)
			if tt.seed > 0 {
				_, err := raw.Exec(`INSERT INTO sequences (key, value) VALUES ('PROJ:', ?)`, tt.seed)
				require.NoError(t, err)
			}

			tt.apply(t, s)

			require.Equal(t, tt.want, sequenceCounter(t, raw, "PROJ:"))
		})
	}
}

// TestIdempotentApply_CreateNodeCounterWriteFails_NothingApplied: when the
// counter cannot be written, the apply fails and writes nothing.
func TestIdempotentApply_CreateNodeCounterWriteFails_NothingApplied(t *testing.T) {
	s, raw := applyTestStore(t)
	_, err := raw.Exec(`CREATE TRIGGER test_refuse_sequence BEFORE INSERT ON sequences
		BEGIN SELECT RAISE(ABORT, 'sequence write refused'); END`)
	require.NoError(t, err)

	err = applyOnce(t, s, pulledCreate(t, "PROJ-3", 10))

	require.ErrorContains(t, err, "sequence write refused")
	require.Zero(t, countNodes(t, raw))
	require.Zero(t, countApplied(t, raw))
}

// projectNode builds the node id with the number seq and the title title;
// its parent and project follow from the id.
func projectNode(id string, seq int, title string) *model.Node {
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	depth := model.ParseIDDepth(id)
	n := &model.Node{
		ID: id, ParentID: model.ParseIDParent(id), Project: model.ParseIDProject(id),
		Depth: depth, Seq: seq, Title: title, NodeType: model.NodeTypeForDepth(depth),
		Priority: model.PriorityMedium, Status: model.StatusOpen, Weight: 1.0,
		Creator: "local", CreatedAt: now, UpdatedAt: now,
	}
	n.ContentHash = n.ComputeHash()
	return n
}

// TestIdempotentApply_CreateNodeIDTakenCounterBehind_CounterAdvanced: a
// different local task already holds PROJ-3, inserted without advancing
// its counter, so the pulled PROJ-3 is not inserted. The apply still
// advances the counter to 3, because the number is taken either way, and
// the next local create takes PROJ-4.
func TestIdempotentApply_CreateNodeIDTakenCounterBehind_CounterAdvanced(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, s.CreateNode(context.Background(), projectNode("PROJ-3", 3, "local task")))
	require.Zero(t, sequenceCounter(t, raw, "PROJ:"))

	require.NoError(t, applyOnce(t, s, pulledCreate(t, "PROJ-3", 10)))

	require.Equal(t, 3, sequenceCounter(t, raw, "PROJ:"))
	require.Equal(t, "local task", readNodeColumn(t, raw, "PROJ-3", "title"), "the local task keeps the id")
	id, err := createLocally(t, s, "", "next")
	require.NoError(t, err)
	require.Equal(t, "PROJ-4", id)
}

// TestIdempotentApply_CreateNodeNumberAboveBound_CounterNotAdvanced: a
// pulled task numbered above maxSequence (2147483647) is applied, but its
// number does not advance the counter, and the apply logs a warning naming
// the task. Before, PROJ-9223372036854775807 set the counter to the int64
// maximum and every later create in the namespace overflowed.
func TestIdempotentApply_CreateNodeNumberAboveBound_CounterNotAdvanced(t *testing.T) {
	for _, id := range []string{"PROJ-2147483648", "PROJ-9223372036854775807"} {
		t.Run(id, func(t *testing.T) {
			logs := captureDefaultLog(t)
			s, raw := applyTestStore(t)

			require.NoError(t, applyOnce(t, s, pulledCreate(t, id, 10)))

			require.Equal(t, 1, countNodes(t, raw), "the task is applied")
			require.Zero(t, sequenceCounter(t, raw, "PROJ:"), "its number is not counted")
			require.Contains(t, logs.String(), "level=WARN")
			require.Contains(t, logs.String(), "node_id="+id)
			root, err := createLocally(t, s, "", "local")
			require.NoError(t, err)
			require.Equal(t, "PROJ-1", root)
		})
	}
}

// TestIdempotentApply_CreateNodeAtBound_NextCreateFailsClearly: a pulled
// number equal to maxSequence advances the counter to it. The namespace
// then has no number left, and the next allocation fails with an error
// that names the limit instead of handing out 2147483648.
func TestIdempotentApply_CreateNodeAtBound_NextCreateFailsClearly(t *testing.T) {
	s, raw := applyTestStore(t)
	require.NoError(t, applyOnce(t, s, pulledCreate(t, "PROJ-2147483647", 10)))
	require.Equal(t, 2147483647, sequenceCounter(t, raw, "PROJ:"))

	_, err := s.NextSequence(context.Background(), "PROJ:")

	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.ErrorContains(t, err, "2147483647")
	require.Equal(t, 2147483647, sequenceCounter(t, raw, "PROJ:"), "the counter is unchanged")
}
