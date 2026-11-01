// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

func TestAutoImport_TeammateDependencyRemoval_Applies(t *testing.T) {
	for _, typ := range model.AllDepTypes() {
		t.Run(string(typ), func(t *testing.T) {
			ctx := context.Background()
			a := dependencyRemovalFixture(t)
			require.NoError(t, a.store.AddDependency(ctx, &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: typ}))
			require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
			b := dependencyRemovalClone(t, a)
			before, err := b.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			now := before.UpdatedAt.Add(time.Hour)
			b.store.SetClock(func() time.Time { return now })
			require.NoError(t, b.store.RemoveDependency(ctx, "TEST-1", "TEST-2", typ))
			after, err := b.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			assert.Equal(t, now, after.UpdatedAt)
			entries, err := b.store.GetActivity(ctx, "TEST-1", 100, 0)
			require.NoError(t, err)
			assert.Equal(t, now, entries[len(entries)-1].CreatedAt)
			require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
			shareBoard(t, b, a)
			require.NoError(t, a.svc.AutoImport(ctx, a.mtixDir), a.notices.String())
			data, err := a.store.Export(ctx, "TEST", "test")
			require.NoError(t, err)
			assert.Empty(t, data.Dependencies)
			assert.Contains(t, a.notices.String(), "dependencies 0 added, 1 removed")
		})
	}
}

func TestAutoImport_StaleBoardMissingDependency_Refused(t *testing.T) {
	ctx := context.Background()
	a := dependencyRemovalFixture(t)
	b := dependencyRemovalClone(t, a)
	require.NoError(t, a.store.AddDependency(ctx, &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeRelated}))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	before := a.storeSnapshot(t)
	// A later unrelated edit must not prove this missing edge was removed.
	title := "Teammate edited a stale source"
	require.NoError(t, b.store.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
	require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
	shareBoard(t, b, a)
	require.ErrorIs(t, a.svc.AutoImport(ctx, a.mtixDir), service.ErrAutoImportRefused)
	assert.Equal(t, before, a.storeSnapshot(t))
	assert.Contains(t, a.notices.String(), "related TEST-2")
}

func dependencyRemovalFixture(t *testing.T) *guardFixture {
	t.Helper()
	f := dependencyRemovalClone(t, nil)
	for i, id := range []string{"TEST-1", "TEST-2"} {
		require.NoError(t, f.store.CreateNode(t.Context(), &model.Node{ID: id, Project: "TEST", Seq: i + 1, Title: "Dependency endpoint " + id, Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1, CreatedAt: f.now, UpdatedAt: f.now}))
	}
	require.NoError(t, f.svc.AutoExport(t.Context(), f.mtixDir))
	return f
}

func dependencyRemovalClone(t *testing.T, from *guardFixture) *guardFixture {
	t.Helper()
	f := newProjectFixture(t)
	_, err := f.store.WriteDB().ExecContext(t.Context(), "UPDATE meta SET value = ? WHERE key = ?", "0123456789abcdef", "meta.sync.machine_hash")
	require.NoError(t, err)
	if from != nil {
		shareBoard(t, from, f)
		require.NoError(t, f.svc.AutoImport(t.Context(), f.mtixDir))
	}
	return f
}

func TestAutoImport_PreviouslyRecordedDependencyRemoval_Refused(t *testing.T) {
	ctx := context.Background()
	a := dependencyRemovalFixture(t)
	dep := &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeRelated}
	require.NoError(t, a.store.AddDependency(ctx, dep))
	require.NoError(t, a.store.RemoveDependency(ctx, dep.FromID, dep.ToID, dep.DepType))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	b := dependencyRemovalClone(t, a)
	require.NoError(t, a.store.AddDependency(ctx, dep))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	title := "An unrelated newer edit with an old removal entry"
	require.NoError(t, b.store.UpdateNode(ctx, dep.FromID, &store.NodeUpdate{Title: &title}))
	require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
	shareBoard(t, b, a)
	require.ErrorIs(t, a.svc.AutoImport(ctx, a.mtixDir), service.ErrAutoImportRefused)
}

func TestRemoveDependency_ActivityFailure_RollsBack(t *testing.T) {
	ctx := context.Background()
	f := dependencyRemovalFixture(t)
	dep := &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeRelated}
	require.NoError(t, f.store.AddDependency(ctx, dep))
	before := f.storeSnapshot(t)
	_, err := f.store.WriteDB().ExecContext(ctx, `CREATE TRIGGER refuse_removal_activity BEFORE UPDATE OF activity ON nodes BEGIN SELECT RAISE(ABORT, 'activity unavailable'); END`)
	require.NoError(t, err)
	require.ErrorContains(t, f.store.RemoveDependency(ctx, dep.FromID, dep.ToID, dep.DepType), "activity unavailable")
	assert.Equal(t, before, f.storeSnapshot(t))
}

func TestRemoveDependency_MissingEdge_LeavesSourceUnchanged(t *testing.T) {
	f := dependencyRemovalFixture(t)
	before := f.storeSnapshot(t)
	require.ErrorIs(t, f.store.RemoveDependency(t.Context(), "TEST-1", "TEST-2", model.DepTypeRelated), model.ErrNotFound)
	assert.Equal(t, before, f.storeSnapshot(t))
}

// TestAutoImport_RemovalEntryFromNonDescendingCopy_StillRefused: a copy that
// carries the new removal entry but lacks a local-only activity entry has not
// seen this machine's edit, so its missing edge is not proof of a deliberate
// removal.
func TestAutoImport_RemovalEntryFromNonDescendingCopy_StillRefused(t *testing.T) {
	ctx := context.Background()
	a := dependencyRemovalFixture(t)
	dep := &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeRelated}
	require.NoError(t, a.store.AddDependency(ctx, dep))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	b := dependencyRemovalClone(t, a)
	require.NoError(t, b.store.RemoveDependency(ctx, dep.FromID, dep.ToID, dep.DepType))
	require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
	// A local-only activity entry on the dependency's source task that the copy never saw.
	require.NoError(t, a.store.ClaimNode(ctx, "TEST-1", "local-agent"))
	shareBoard(t, b, a)
	require.ErrorIs(t, a.svc.AutoImport(ctx, a.mtixDir), service.ErrAutoImportRefused)
	assert.Contains(t, a.notices.String(), "related TEST-2",
		"the missing edge stays listed as a loss: the copy does not descend from the local task")
}

// TestAutoImport_RemovalOfAnotherEdge_DoesNotProveAMissingOne: the copy's new
// removal entry names the blocks edge only, so an unrelated edge that is
// missing from the copy stays a loss.
func TestAutoImport_RemovalOfAnotherEdge_DoesNotProveAMissingOne(t *testing.T) {
	ctx := context.Background()
	a := dependencyRemovalFixture(t)
	blocks := &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks}
	related := &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeRelated}
	require.NoError(t, a.store.AddDependency(ctx, blocks))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	b := dependencyRemovalClone(t, a)
	// The related edge exists only locally (added after the teammate copied the board).
	require.NoError(t, a.store.AddDependency(ctx, related))
	require.NoError(t, b.store.RemoveDependency(ctx, blocks.FromID, blocks.ToID, blocks.DepType))
	require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
	shareBoard(t, b, a)
	require.ErrorIs(t, a.svc.AutoImport(ctx, a.mtixDir), service.ErrAutoImportRefused)
	assert.Contains(t, a.notices.String(), "related TEST-2")
}
