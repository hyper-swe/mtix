// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// mtix sync reconcile --rename-to and --import-as rewrite the ids of the
// local tree into new namespaces (MTIX-95.38). They raise the counter of
// every parent key in the new namespaces in their own transaction, so the
// first create after either command takes the next free number. They
// leave the project, seq and depth columns as they were (MTIX-107.58), so
// the skip in NextSequence reads the taken numbers from the ids.

// createUnder creates a task the way a local create does: a root of
// project when parentID is empty, otherwise a child of parentID in the
// parent's project (the project column the parent holds). It returns the
// id and the insert's error.
func createUnder(t *testing.T, s *Store, project, parentID string) (string, error) {
	t.Helper()
	ctx := context.Background()
	if parentID != "" {
		parent, err := s.GetNode(ctx, parentID)
		require.NoError(t, err)
		project = parent.Project
	}
	seq, err := s.NextSequence(ctx, sequenceKey(project, parentID))
	require.NoError(t, err)
	depth := computeDepth(parentID)
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	n := &model.Node{
		ID: model.BuildID(project, parentID, seq), ParentID: parentID, Project: project,
		Depth: depth, Seq: seq, Title: "new under " + parentID, NodeType: model.NodeTypeForDepth(depth),
		Priority: model.PriorityMedium, Status: model.StatusOpen, Weight: 1.0,
		Creator: "local", CreatedAt: now, UpdatedAt: now,
	}
	n.ContentHash = n.ComputeHash()
	return n.ID, s.CreateNode(ctx, n)
}

// reconcileCase is one reconcile command, the counters it must leave, and
// the ids the first create under each parent must take.
type reconcileCase struct {
	name     string
	seed     func(t *testing.T, s *Store)
	run      func(s *Store, mtixDir string) error
	counters map[string]int
	creates  []reconcileCreate
}

// reconcileCreate is one create after the command: a root of project
// when parent is empty, else a child of parent; want is the id it takes.
type reconcileCreate struct {
	project, parent, want string
}

// seedTreeWithStrayChild seeds seedTree plus two rows whose parent_id
// names MTIX-1 although their ids are not '<MTIX-1>.<digits>': MTIX-9.9,
// outside MTIX-1's namespace, and MTIX-1.7.1, shaped as a grandchild. Neither
// may raise the counter of MTIX-1 (renamed DEMO-1) to 9 or 7.
func seedTreeWithStrayChild(t *testing.T, s *Store) {
	t.Helper()
	seedTree(t, s)
	for _, id := range []string{"MTIX-9.9", "MTIX-1.7.1"} {
		stray := projectNode(id, 9, "stray")
		stray.ParentID = "MTIX-1"
		require.NoError(t, s.CreateNode(context.Background(), stray))
	}
}

// reconcileCases: --rename-to DEMO over seedTreeWithStrayChild (MTIX-1 with
// MTIX-1.1, MTIX-1.1.1 and MTIX-1.2, and MTIX-2, all with seq 0, and the
// strays MTIX-9.9 and MTIX-1.7.1), and --import-as
// PROJ-7 of the roots MTIX-4 (child MTIX-4.1) and MTIX-9, which become
// PROJ-7.1 (PROJ-7.1.1) and PROJ-7.2 but keep the seq columns 4, 1 and 9.
// A child key names the parent's project column, as a create under it
// does: MTIX for the renamed and imported tasks, PROJ for PROJ-7.
func reconcileCases() []reconcileCase {
	return []reconcileCase{
		{"rename-to", seedTreeWithStrayChild, func(s *Store, mtixDir string) error {
			_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
			return err
		}, map[string]int{"DEMO:": 2, "MTIX:DEMO-1": 2, "MTIX:DEMO-1.1": 1}, []reconcileCreate{
			{"DEMO", "", "DEMO-3"}, {"", "DEMO-1", "DEMO-1.3"}, {"", "DEMO-1.1", "DEMO-1.1.2"},
		}},
		{"import-as", func(t *testing.T, s *Store) {
			seedParentForImport(t, s, "PROJ-7")
			for _, n := range []*model.Node{
				projectNode("MTIX-4", 4, "root four"), projectNode("MTIX-4.1", 1, "child"),
				projectNode("MTIX-9", 9, "root nine"),
			} {
				require.NoError(t, s.CreateNode(context.Background(), n))
			}
		}, func(s *Store, mtixDir string) error {
			_, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
			return err
		}, map[string]int{"PROJ:PROJ-7": 2, "MTIX:PROJ-7.1": 1}, []reconcileCreate{
			{"", "PROJ-7", "PROJ-7.3"}, {"", "PROJ-7.1", "PROJ-7.1.2"},
		}},
	}
}

// TestReconcile_NewNamespaces_CountersRaisedFirstCreateSucceeds: after
// each command the counters of the new namespaces cover the ids in them,
// and the first create under each parent succeeds with the next number.
// Before, both commands wrote no counter: the first root create after
// rename-to failed with "node DEMO-2 already exists", and the first create
// under PROJ-7 after import-as with "node PROJ-7.2 already exists".
func TestReconcile_NewNamespaces_CountersRaisedFirstCreateSucceeds(t *testing.T) {
	for _, tc := range reconcileCases() {
		t.Run(tc.name, func(t *testing.T) {
			s, raw, mtixDir := reconcileTestStore(t)
			tc.seed(t, s)

			require.NoError(t, tc.run(s, mtixDir))

			for key, want := range tc.counters {
				require.Equal(t, want, sequenceCounter(t, raw, key), key)
			}
			for _, c := range tc.creates {
				id, err := createUnder(t, s, c.project, c.parent)
				require.NoError(t, err, "the first create under %q succeeds", c.parent)
				require.Equal(t, c.want, id)
			}
		})
	}
}

// TestReconcile_CountersBehindAfterCommand_CreateSkipsByIDs: with the
// counters removed after the command (as a store reconciled before
// MTIX-95.38 has them), the first create under each parent reaches a taken
// number and the skip moves past the highest number the ids hold, although
// the project and seq columns still hold the old values.
func TestReconcile_CountersBehindAfterCommand_CreateSkipsByIDs(t *testing.T) {
	for _, tc := range reconcileCases() {
		t.Run(tc.name, func(t *testing.T) {
			s, raw, mtixDir := reconcileTestStore(t)
			tc.seed(t, s)
			require.NoError(t, tc.run(s, mtixDir))
			_, err := raw.Exec(`DELETE FROM sequences`)
			require.NoError(t, err)

			for _, c := range tc.creates {
				id, createErr := createUnder(t, s, c.project, c.parent)
				require.NoError(t, createErr, "the first create under %q succeeds", c.parent)
				require.Equal(t, c.want, id)
			}
		})
	}
}

// TestReconcile_CounterWriteFails_NothingRenamed: when a counter cannot be
// written, the command fails and its transaction renames nothing.
func TestReconcile_CounterWriteFails_NothingRenamed(t *testing.T) {
	for _, tc := range reconcileCases() {
		t.Run(tc.name, func(t *testing.T) {
			s, raw, mtixDir := reconcileTestStore(t)
			tc.seed(t, s)
			before := readNodeIDs(t, raw)
			_, err := raw.Exec(`CREATE TRIGGER test_refuse_sequence BEFORE INSERT ON sequences
				BEGIN SELECT RAISE(ABORT, 'sequence write refused'); END`)
			require.NoError(t, err)

			err = tc.run(s, mtixDir)

			require.ErrorContains(t, err, "sequence write refused")
			require.Equal(t, before, readNodeIDs(t, raw))
		})
	}
}
