// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// reconcileExport assembles an ExportData from the given nodes with a valid
// checksum and node_count so it passes Import's integrity gates.
func reconcileExport(t *testing.T, project string, nodes ...sqlite.TestExportNode) *sqlite.ExportData {
	t.Helper()
	data := sqlite.MakeExportData(project, nodes...)
	data.Checksum = sqlite.RecomputeChecksumForTest(t, data)
	return data
}

// provisionalPath builds a provisional (uid-bearing) display_path under parent.
func provisionalPath(t *testing.T, parent, uid string) string {
	t.Helper()
	p, err := model.BuildProvisionalID(parent, uid)
	require.NoError(t, err)
	return p
}

// TestImportReconcile_IdempotentReimport verifies that re-importing a node with
// an identical uid + identical display_path is an idempotent no-op (ADR-003 §6).
func TestImportReconcile_IdempotentReimport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := newUID(t)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Existing",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", UID: uid,
		CreatedAt: now, UpdatedAt: now,
	}))

	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Existing",
		ContentHash: "h1", UID: uid, CreatedAt: now, UpdatedAt: now,
	})

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	assert.Empty(t, report.Conflicts, "identical uid+path must not conflict")
	assert.Equal(t, 1, report.Idempotent, "identical uid+path is a no-op")
	assert.True(t, report.Applied)
}

// TestImportReconcile_LocalUIDCollisionRejected verifies that an incoming uid
// that duplicates an existing LOCAL node with a DIFFERENT display_path is
// rejected loudly and nothing is mutated (ADR-003 §6, audit F-3).
func TestImportReconcile_LocalUIDCollisionRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := newUID(t)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Local owner",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", UID: uid,
		CreatedAt: now, UpdatedAt: now,
	}))

	// Incoming node reuses the same uid but a different display_path.
	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-2", Project: "REC", Depth: 0, Seq: 2, Title: "Intruder",
		ContentHash: "h2", UID: uid, CreatedAt: now, UpdatedAt: now,
	})

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrConflict)
	require.NotNil(t, report)
	require.Len(t, report.Conflicts, 1)
	assert.Equal(t, uid, report.Conflicts[0].UID)
	assert.Equal(t, "REC-2", report.Conflicts[0].ImportPath)
	assert.Equal(t, "REC-1", report.Conflicts[0].LocalPath)
	assert.False(t, report.Applied, "nothing must be applied on rejection")

	// The intruder must NOT have been created.
	_, getErr := s.GetNode(ctx, "REC-2")
	assert.ErrorIs(t, getErr, model.ErrNotFound)
}

// TestImportReconcile_CraftedDuplicateUIDRejected verifies that an export which
// itself contains two nodes sharing one uid is detected and rejected, never
// silently linked (ADR-003 §6, audit F-3).
func TestImportReconcile_CraftedDuplicateUIDRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := newUID(t)

	data := reconcileExport(t, "REC",
		sqlite.TestExportNode{
			ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "A",
			ContentHash: "h1", UID: uid, CreatedAt: now, UpdatedAt: now,
		},
		sqlite.TestExportNode{
			ID: "REC-2", Project: "REC", Depth: 0, Seq: 2, Title: "B",
			ContentHash: "h2", UID: uid, CreatedAt: now, UpdatedAt: now,
		},
	)

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeReplace, Confirm: true,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrConflict)
	require.NotNil(t, report)
	require.Len(t, report.Conflicts, 1)
	assert.Equal(t, uid, report.Conflicts[0].UID)
	assert.False(t, report.Applied)

	// Neither node should have been created.
	_, e1 := s.GetNode(ctx, "REC-1")
	assert.ErrorIs(t, e1, model.ErrNotFound)
}

// TestImportReconcile_ForceRenameRestampsColliding verifies that --force-rename
// re-stamps the colliding IMPORT node with a locally-minted uid and applies it
// rather than rejecting (ADR-003 §6, audit F-3).
func TestImportReconcile_ForceRenameRestampsColliding(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := newUID(t)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Local owner",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", UID: uid,
		CreatedAt: now, UpdatedAt: now,
	}))

	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-2", Project: "REC", Depth: 0, Seq: 2, Title: "Intruder",
		ContentHash: "h2", UID: uid, CreatedAt: now, UpdatedAt: now,
	})

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, ForceRename: true, Confirm: true,
	})
	require.NoError(t, err)
	assert.Empty(t, report.Conflicts, "force-rename resolves the collision")
	require.Len(t, report.Renamed, 1)
	assert.Equal(t, "REC-2", report.Renamed[0].OldPath)
	assert.True(t, report.Applied)

	// Both nodes exist; the import node carries a fresh, distinct uid.
	local, e1 := s.GetNode(ctx, "REC-1")
	require.NoError(t, e1)
	imported, e2 := s.GetNode(ctx, "REC-2")
	require.NoError(t, e2)
	assert.Equal(t, uid, local.UID)
	assert.NotEqual(t, uid, imported.UID, "import node must be re-stamped")
	assert.NotEmpty(t, imported.UID)
}

// TestImportReconcile_ProvisionalRenumbered verifies that incoming provisional
// (uid-bearing) nodes are renumbered to clean local numbers and a uid-keyed
// remap is produced; references resolve via uid after import (ADR-003 §6).
func TestImportReconcile_ProvisionalRenumbered(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Local store already owns REC-1 and REC-1.1 (clean numbers).
	rootUID := newUID(t)
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "r1", UID: rootUID,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1.1", ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
		Title: "Existing child", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeStory,
		ContentHash: "c1", UID: newUID(t), CreatedAt: now, UpdatedAt: now,
	}))

	// Incoming: a provisional child under REC-1 whose clean number 1 clashes.
	provUID := newUID(t)
	provID := provisionalPath(t, "REC-1", provUID)
	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: provID, ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
		Title: "Provisional incoming", ContentHash: "p1", UID: provUID,
		CreatedAt: now, UpdatedAt: now,
	})

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	require.Len(t, report.Remaps, 1)
	assert.Equal(t, provUID, report.Remaps[0].UID)
	assert.Equal(t, provID, report.Remaps[0].OldPath)
	// Clean target 1 is taken, so it must land at REC-1.2.
	assert.Equal(t, "REC-1.2", report.Remaps[0].NewPath)
	assert.True(t, report.Applied)

	// Reference resolves via uid to the new clean path.
	resolved, rerr := s.ResolveDisplayPathByUID(ctx, provUID)
	require.NoError(t, rerr)
	assert.Equal(t, "REC-1.2", resolved)
	assert.False(t, model.IsProvisional(resolved), "imported node must be settled")
}

// TestImportReconcile_LiveStoreRequiresConfirmation verifies that a renumber
// against a non-empty live store is NOT applied without explicit confirmation;
// the report is returned and the store is untouched (ADR-003 §6).
func TestImportReconcile_LiveStoreRequiresConfirmation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	rootUID := newUID(t)
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "r1", UID: rootUID,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1.1", ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
		Title: "Existing child", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeStory,
		ContentHash: "c1", UID: newUID(t), CreatedAt: now, UpdatedAt: now,
	}))

	provUID := newUID(t)
	provID := provisionalPath(t, "REC-1", provUID)
	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: provID, ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
		Title: "Provisional incoming", ContentHash: "p1", UID: provUID,
		CreatedAt: now, UpdatedAt: now,
	})

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: false,
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
	require.NotNil(t, report)
	assert.False(t, report.Applied, "must not mutate without confirmation")
	require.Len(t, report.Remaps, 1, "remap is still reported for review")

	// The provisional node must NOT have been created.
	_, getErr := s.ResolveDisplayPathByUID(ctx, provUID)
	assert.ErrorIs(t, getErr, model.ErrNotFound)
}

// TestImportReconcile_EmptyStoreNoConfirmNeeded verifies that importing into an
// empty store does not require confirmation (nothing existing to clobber).
func TestImportReconcile_EmptyStoreNoConfirmNeeded(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	uid := newUID(t)
	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Fresh",
		ContentHash: "h1", UID: uid, CreatedAt: now, UpdatedAt: now,
	})

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeReplace, Confirm: false,
	})
	require.NoError(t, err, "empty store import needs no confirmation")
	assert.True(t, report.Applied)

	got, gerr := s.GetNode(ctx, "REC-1")
	require.NoError(t, gerr)
	assert.Equal(t, uid, got.UID, "import must persist the incoming uid")
}

// TestImportReconcile_RemapReportString verifies the loud report renders the
// conflicts and remaps for human review (ADR-003 §6 — loud report).
func TestImportReconcile_RemapReportString(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	rootUID := newUID(t)
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "r1", UID: rootUID,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1.1", ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
		Title: "Existing child", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeStory,
		ContentHash: "c1", UID: newUID(t), CreatedAt: now, UpdatedAt: now,
	}))

	provUID := newUID(t)
	provID := provisionalPath(t, "REC-1", provUID)
	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: provID, ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
		Title: "Provisional incoming", ContentHash: "p1", UID: provUID,
		CreatedAt: now, UpdatedAt: now,
	})

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	out := report.String()
	assert.Contains(t, out, provUID)
	assert.Contains(t, out, "REC-1.2")
}

// TestImportReconcile_NilDataRejected verifies defensive input validation.
func TestImportReconcile_NilDataRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, _, err := s.ImportReconcile(ctx, nil, sqlite.ImportReconcileOptions{})
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestImportReconcile_ProvisionalSubtreeRebased verifies that a provisional
// PARENT and its numeric descendants are renumbered together: the descendants
// follow their renumbered ancestor onto a clean path, and every node resolves
// via uid afterward (ADR-003 §6 — the remap-file rebasing case).
func TestImportReconcile_ProvisionalSubtreeRebased(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Local store owns REC-1 and REC-1.1.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "r1", UID: newUID(t),
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1.1", ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
		Title: "Existing", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1.0, NodeType: model.NodeTypeStory, ContentHash: "c1",
		UID: newUID(t), CreatedAt: now, UpdatedAt: now,
	}))

	// Incoming provisional parent under REC-1 with a numeric grandchild + a
	// deeper great-grandchild, so applyRebase must follow the longest ancestor.
	parentUID := newUID(t)
	childUID := newUID(t)
	grandUID := newUID(t)
	parentID := provisionalPath(t, "REC-1", parentUID)
	childID := parentID + ".1"
	grandID := parentID + ".1.1"

	data := reconcileExport(t, "REC",
		sqlite.TestExportNode{
			ID: parentID, ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
			Title: "Prov parent", ContentHash: "pp", UID: parentUID,
			CreatedAt: now, UpdatedAt: now,
		},
		sqlite.TestExportNode{
			ID: childID, ParentID: parentID, Project: "REC", Depth: 2, Seq: 1,
			Title: "Child", ContentHash: "cc", UID: childUID,
			CreatedAt: now, UpdatedAt: now,
		},
		sqlite.TestExportNode{
			ID: grandID, ParentID: childID, Project: "REC", Depth: 3, Seq: 1,
			Title: "Grand", ContentHash: "gg", UID: grandUID,
			CreatedAt: now, UpdatedAt: now,
		},
	)

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	require.True(t, report.Applied)
	// Every node in the provisional subtree carries a uid-bearing ancestor
	// segment, so all three are provisional by shape and each is remapped; the
	// depth-ordered plan rebases each onto its already-renumbered parent.
	require.Len(t, report.Remaps, 3)
	assert.Equal(t, "REC-1.2", report.Remaps[0].NewPath)

	// The whole subtree resolves to clean, settled paths via uid.
	for uid, want := range map[string]string{
		parentUID: "REC-1.2", childUID: "REC-1.2.1", grandUID: "REC-1.2.1.1",
	} {
		got, rerr := s.ResolveDisplayPathByUID(ctx, uid)
		require.NoError(t, rerr)
		assert.Equal(t, want, got)
		assert.False(t, model.IsProvisional(got))
	}
}

// TestImportReconcile_TwoProvisionalsGetDistinctNumbers verifies that two
// sibling provisional incoming nodes are assigned DISTINCT clean numbers even
// before either is written (ADR-003 §6 — deterministic in-plan reservation).
func TestImportReconcile_TwoProvisionalsGetDistinctNumbers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "r1", UID: newUID(t),
		CreatedAt: now, UpdatedAt: now,
	}))

	a, b := newUID(t), newUID(t)
	idA := provisionalPath(t, "REC-1", a)
	idB := provisionalPath(t, "REC-1", b)
	data := reconcileExport(t, "REC",
		sqlite.TestExportNode{
			ID: idA, ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
			Title: "A", ContentHash: "a", UID: a, CreatedAt: now, UpdatedAt: now,
		},
		sqlite.TestExportNode{
			ID: idB, ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
			Title: "B", ContentHash: "b", UID: b, CreatedAt: now, UpdatedAt: now,
		},
	)

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	require.Len(t, report.Remaps, 2)
	pA, _ := s.ResolveDisplayPathByUID(ctx, a)
	pB, _ := s.ResolveDisplayPathByUID(ctx, b)
	assert.NotEqual(t, pA, pB, "two provisionals must get distinct numbers")
	assert.ElementsMatch(t, []string{"REC-1.1", "REC-1.2"}, []string{pA, pB})
}

// TestImportReconcile_ApplyFailureReturnsReport verifies that a failure in the
// final apply step (here a dangling dependency FK) returns the loud report and a
// non-applied result, leaving the live store untouched (ADR-003 §6, FR-15.2d).
func TestImportReconcile_ApplyFailureReturnsReport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "r1", UID: newUID(t),
		CreatedAt: now, UpdatedAt: now,
	}))

	provUID := newUID(t)
	provID := provisionalPath(t, "REC-1", provUID)
	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: provID, ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
		Title: "Prov", ContentHash: "p1", UID: provUID,
		CreatedAt: now, UpdatedAt: now,
	})
	// Inject a dependency to a non-existent node so the apply transaction fails.
	data.Dependencies = append(data.Dependencies, sqlite.MakeExportDep(
		provID, "NOPE-99", "blocks", now.Format("2006-01-02T15:04:05Z")))
	data.Checksum = sqlite.RecomputeChecksumForTest(t, data)

	report, result, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeReplace, Confirm: true,
	})
	require.Error(t, err)
	assert.Nil(t, result)
	require.NotNil(t, report)
	assert.False(t, report.Applied)
	// Renumber was planned (report is loud) but nothing committed.
	require.Len(t, report.Remaps, 1)
	_, resErr := s.ResolveDisplayPathByUID(ctx, provUID)
	assert.ErrorIs(t, resErr, model.ErrNotFound)
}

// TestImportReconcile_PartialSubtreeRebasedByPrefix verifies the longest-prefix
// rebase: a provisional node whose parent path sits UNDER a renumbered ancestor
// but is itself absent from the export (a partial/gappy subtree) still follows
// its renumbered ancestor (ADR-003 §6).
func TestImportReconcile_PartialSubtreeRebasedByPrefix(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "r1", UID: newUID(t),
		CreatedAt: now, UpdatedAt: now,
	}))

	parentUID := newUID(t)
	deepUID := newUID(t)
	parentID := provisionalPath(t, "REC-1", parentUID)
	// The intermediate "<parent>.5" is NOT exported; the deep node references it.
	deepID := parentID + ".5.1"

	data := reconcileExport(t, "REC",
		sqlite.TestExportNode{
			ID: parentID, ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
			Title: "Prov parent", ContentHash: "pp", UID: parentUID,
			CreatedAt: now, UpdatedAt: now,
		},
		sqlite.TestExportNode{
			ID: deepID, ParentID: parentID + ".5", Project: "REC", Depth: 3, Seq: 1,
			Title: "Deep", ContentHash: "dd", UID: deepUID,
			CreatedAt: now, UpdatedAt: now,
		},
	)

	// The deep node is rebased onto the renumbered ancestor (REC-1.1.5.1, proving
	// the longest-prefix rebase). Its intermediate parent REC-1.1.5 is absent
	// from this gappy export, so the FK-checked apply rejects it — integrity is
	// preserved rather than a dangling row written.
	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.Error(t, err)
	assert.False(t, report.Applied)
	require.Len(t, report.Remaps, 2)
	// The remap shows the deep node was rebased onto the renumbered ancestor.
	var deepNew string
	for _, m := range report.Remaps {
		if m.UID == deepUID {
			deepNew = m.NewPath
		}
	}
	assert.Equal(t, "REC-1.1.5.1", deepNew, "deep node rebased via longest-prefix")
}

// TestImportConflictKind_String verifies the conflict-kind labels used in the
// loud report (ADR-003 §6).
func TestImportConflictKind_String(t *testing.T) {
	assert.Equal(t, "uid collides with a different local node",
		sqlite.ConflictLocalUIDMismatch.String())
	assert.Equal(t, "duplicate uid within the export",
		sqlite.ConflictExportDuplicateUID.String())
	assert.Equal(t, "unknown conflict", sqlite.ImportConflictKind(99).String())
}

// TestImportReconcile_ConflictReportString verifies the loud report enumerates
// rejected conflicts (ADR-003 §6).
func TestImportReconcile_ConflictReportString(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := newUID(t)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Owner",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", UID: uid,
		CreatedAt: now, UpdatedAt: now,
	}))
	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-2", Project: "REC", Depth: 0, Seq: 2, Title: "Intruder",
		ContentHash: "h2", UID: uid, CreatedAt: now, UpdatedAt: now,
	})

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.Error(t, err)
	out := report.String()
	assert.Contains(t, out, "REJECTED uid conflicts")
	assert.Contains(t, out, uid)
}

// TestImportReconcile_ForceRenameReportString verifies the re-stamp section of
// the loud report under --force-rename (ADR-003 §6).
func TestImportReconcile_ForceRenameReportString(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	uid := newUID(t)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "Owner",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", UID: uid,
		CreatedAt: now, UpdatedAt: now,
	}))
	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-2", Project: "REC", Depth: 0, Seq: 2, Title: "Intruder",
		ContentHash: "h2", UID: uid, CreatedAt: now, UpdatedAt: now,
	})

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, ForceRename: true, Confirm: true,
	})
	require.NoError(t, err)
	out := report.String()
	assert.Contains(t, out, "force-renamed")
	assert.Contains(t, out, "REC-2")
}

// TestImportReconcile_MissingUIDSkipsValidation verifies that pre-v3 exports
// (nodes without a uid) import without UID validation but are not silently
// linked under a shared empty uid.
func TestImportReconcile_MissingUIDImportsCleanly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	data := reconcileExport(t, "REC",
		sqlite.TestExportNode{
			ID: "REC-1", Project: "REC", Depth: 0, Seq: 1, Title: "No uid A",
			ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
		},
		sqlite.TestExportNode{
			ID: "REC-2", Project: "REC", Depth: 0, Seq: 2, Title: "No uid B",
			ContentHash: "h2", CreatedAt: now, UpdatedAt: now,
		},
	)

	report, _, err := s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeReplace, Confirm: true,
	})
	require.NoError(t, err, "empty uids must not count as a duplicate-uid collision")
	assert.True(t, report.Applied)
	assert.Empty(t, report.Conflicts)
}
