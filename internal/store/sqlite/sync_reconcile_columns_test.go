// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// mtix sync reconcile --rename-to and --import-as move tasks to new ids.
// Every moved task's project, seq and depth columns follow its new id and
// position (MTIX-107.58), so list, tree, stats, sequence allocation and
// the depth of a new child read the right values.

// movedColumns is the project, seq and depth a task must hold.
type movedColumns struct {
	project    string
	seq, depth int
}

// readMovedColumns returns the project, seq and depth of the task id.
func readMovedColumns(t *testing.T, s *Store, id string) movedColumns {
	t.Helper()
	var c movedColumns
	require.NoError(t, s.readDB.QueryRow(
		`SELECT project, seq, depth FROM nodes WHERE id = ?`, id,
	).Scan(&c.project, &c.seq, &c.depth), id)
	return c
}

// movedColumnsCase is one reconcile command, the columns every task must
// hold after it, the project that holds them all, and one tree read.
type movedColumnsCase struct {
	name     string
	seed     []*model.Node
	parent   string // the clone target seeded before seed, "" for none
	run      func(s *Store, mtixDir string) error
	want     map[string]movedColumns
	project  string
	treeRoot string
	treeIDs  []string // GetTree(treeRoot, 1)
}

// movedColumnsCases: --rename-to DEMO of MTIX-1 (MTIX-1.1, MTIX-1.1.1)
// and MTIX-2, and --import-as PROJ-7 of the roots MTIX-4 (MTIX-4.1,
// MTIX-4.1.1) and MTIX-9. Every seeded row holds the columns of its id.
func movedColumnsCases() []movedColumnsCase {
	return []movedColumnsCase{
		{
			name: "rename-to",
			seed: []*model.Node{
				projectNode("MTIX-1", 1, "root one"), projectNode("MTIX-1.1", 1, "child"),
				projectNode("MTIX-1.1.1", 1, "grandchild"), projectNode("MTIX-2", 2, "root two"),
			},
			run: func(s *Store, mtixDir string) error {
				_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
				return err
			},
			want: map[string]movedColumns{
				"DEMO-1": {"DEMO", 1, 0}, "DEMO-1.1": {"DEMO", 1, 1},
				"DEMO-1.1.1": {"DEMO", 1, 2}, "DEMO-2": {"DEMO", 2, 0},
			},
			project: "DEMO", treeRoot: "DEMO-1", treeIDs: []string{"DEMO-1", "DEMO-1.1"},
		},
		{
			name:   "import-as",
			parent: "PROJ-7",
			seed: []*model.Node{
				projectNode("MTIX-4", 4, "root four"), projectNode("MTIX-4.1", 1, "child"),
				projectNode("MTIX-4.1.1", 1, "grandchild"), projectNode("MTIX-9", 9, "root nine"),
			},
			run: func(s *Store, mtixDir string) error {
				_, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
				return err
			},
			want: map[string]movedColumns{
				"PROJ-7": {"PROJ", 0, 0}, "PROJ-7.1": {"PROJ", 1, 1},
				"PROJ-7.1.1": {"PROJ", 1, 2}, "PROJ-7.1.1.1": {"PROJ", 1, 3},
				"PROJ-7.2": {"PROJ", 2, 1},
			},
			project: "PROJ", treeRoot: "PROJ-7", treeIDs: []string{"PROJ-7", "PROJ-7.1", "PROJ-7.2"},
		},
	}
}

// TestReconcile_MovedNodes_ProjectSeqDepthFollowNewID: after each command
// every task's project, seq and depth match its new id and position, the
// project filter and the project list see the moved tasks in their new
// project, and a one-level tree holds exactly the direct children. Before,
// both commands left the columns as they were: DEMO-1 kept project MTIX,
// and PROJ-7.1 kept project MTIX, seq 4 and depth 0.
func TestReconcile_MovedNodes_ProjectSeqDepthFollowNewID(t *testing.T) {
	for _, tc := range movedColumnsCases() {
		t.Run(tc.name, func(t *testing.T) {
			s, _, mtixDir := reconcileTestStore(t)
			ctx := context.Background()
			if tc.parent != "" {
				seedParentForImport(t, s, tc.parent)
			}
			for _, n := range tc.seed {
				require.NoError(t, s.CreateNode(ctx, n))
			}

			require.NoError(t, tc.run(s, mtixDir))

			for id, want := range tc.want {
				require.Equal(t, want, readMovedColumns(t, s, id), id)
			}
			projects, err := s.DistinctProjects(ctx)
			require.NoError(t, err)
			require.Equal(t, []store.ProjectInfo{{Prefix: tc.project, Count: len(tc.want)}}, projects)
			_, total, err := s.ListNodes(ctx, store.NodeFilter{Project: tc.project}, store.ListOptions{Limit: 100})
			require.NoError(t, err)
			require.Equal(t, len(tc.want), total)
			tree, err := s.GetTree(ctx, tc.treeRoot, 1)
			require.NoError(t, err)
			ids := make([]string, 0, len(tree))
			for _, n := range tree {
				ids = append(ids, n.ID)
			}
			require.ElementsMatch(t, tc.treeIDs, ids)
		})
	}
}
