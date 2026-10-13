// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-95.31.15.1: the first pull after upgrading from mtix 0.5.3 or older.
// The released side is a real binary built from its tag.
package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestAutoImportUpgrade_053Sequence_TeammateClaimAppliedNothingLost(t *testing.T) {
	r := newUpgradeRig(t, releasedMtix(t, tag053, tag053Commit))
	a := r.setupSequence()
	r.pull(a)
	cur := upgrade(t, a)
	_, _, ann0, act0 := nodeOf(t, cur.export(t), "PROJ-1")
	require.GreaterOrEqual(t, ann0, 1, "the released version kept alice's comment in the store")
	require.GreaterOrEqual(t, act0, 1)

	require.NoError(t, cur.svc.AutoImport(context.Background(), cur.mtixDir), cur.notices.String())

	after := cur.export(t)
	st1, as1, ann1, act1 := nodeOf(t, after, "PROJ-1")
	assert.Equal(t, "in_progress", st1)
	assert.Equal(t, "alice", as1)
	assert.Equal(t, ann0, ann1, "no local annotation lost")
	assert.GreaterOrEqual(t, act1, act0, "no local activity lost")
	st2, as2, _, _ := nodeOf(t, after, "PROJ-2")
	assert.Equal(t, "in_progress", st2, "the teammate's claim is applied")
	assert.Equal(t, "bob", as2)
	assert.True(t, mentions(cur.notices, "kept here"), cur.notices.String())
}

func TestAutoImportUpgrade_030Variant_Imports(t *testing.T) {
	r := newUpgradeRig(t, releasedMtix(t, tag030, tag030Commit))
	a := r.setupSequence()
	r.pull(a)
	cur := upgrade(t, a)
	_, _, ann0, _ := nodeOf(t, cur.export(t), "PROJ-1")

	require.NoError(t, cur.svc.AutoImport(context.Background(), cur.mtixDir), cur.notices.String())

	after := cur.export(t)
	_, _, ann1, _ := nodeOf(t, after, "PROJ-1")
	assert.Equal(t, ann0, ann1)
	st2, as2, _, _ := nodeOf(t, after, "PROJ-2")
	assert.Equal(t, "in_progress", st2)
	assert.Equal(t, "bob", as2)
}

func TestAutoImportUpgrade_RecordPresent_UsedBeforeMergeBase(t *testing.T) {
	r := newUpgradeRig(t, releasedMtix(t, tag053, tag053Commit))
	a := r.clone()
	r.startOldProject(a)
	b := r.clone()
	r.old(b, "list")
	r.old(b, "create", "Task three")
	r.old(b, "claim", "PROJ-2", "--agent", "bob") // the teammate still on the released version
	r.publish(b, "board from the teammate")
	r.pull(a)
	first := upgrade(t, a) // the first pull: no record yet, the merge-base board is the base
	require.NoError(t, first.svc.AutoImport(context.Background(), first.mtixDir), first.notices.String())
	assert.True(t, mentions(first.notices, "the git merge-base board"), first.notices.String())
	rec, recErr := first.svc.LastExchanged(context.Background(), first.mtixDir)
	require.NoError(t, recErr, "the first import writes the exchange record")
	assert.Equal(t, "schema-1.0.0", rec.BoardForm)
	require.NoError(t, first.store.Close())

	r.teammateUpgrades(b, "PROJ-3") // B upgrades and pushes a 2.0.0 board
	r.pull(a)
	second := upgrade(t, a)
	require.NoError(t, second.svc.AutoImport(context.Background(), second.mtixDir), second.notices.String())
	assert.True(t, mentions(second.notices, "the last-exchanged record"),
		"the record written by the first import is the base: %s", second.notices.String())
}

func TestAutoImportUpgrade_UnpushedLocalChange_KeptViaMergeBase(t *testing.T) {
	r := newUpgradeRig(t, releasedMtix(t, tag053, tag053Commit))
	a := r.clone()
	r.startOldProject(a)
	b := r.clone()
	r.old(b, "list")
	r.old(a, "update", "PROJ-2", "--title", "Renamed before the upgrade") // never pushed
	r.discardBoard(a)
	r.teammateUpgrades(b, "PROJ-2")
	r.pull(a)
	cur := upgrade(t, a)

	require.NoError(t, cur.svc.AutoImport(context.Background(), cur.mtixDir), cur.notices.String())

	after := cur.export(t)
	for i := range after.Nodes {
		if after.Nodes[i].ID == "PROJ-2" {
			assert.Equal(t, "Renamed before the upgrade", after.Nodes[i].Title, "the unpushed change is kept")
			assert.Equal(t, "bob", after.Nodes[i].Assignee, "the teammate's claim is applied")
		}
	}
	assert.True(t, mentions(cur.notices, "the git merge-base board"), cur.notices.String())
}

func TestAutoImportUpgrade_GenuineLocalLoss_Refused(t *testing.T) {
	r := newUpgradeRig(t, releasedMtix(t, tag053, tag053Commit))
	a := r.clone()
	r.startOldProject(a)
	b := r.clone()
	r.old(b, "list")
	r.old(a, "dep", "add", "PROJ-1", "PROJ-2") // a dependency the board never held
	r.discardBoard(a)
	r.teammateUpgrades(b, "PROJ-2")
	r.pull(a)
	cur := upgrade(t, a)
	before := cur.export(t)

	err := cur.svc.AutoImport(context.Background(), cur.mtixDir)

	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	assert.True(t, mentions(cur.notices, "PROJ-1", "dependency"), cur.notices.String())
	assert.Equal(t, before.Checksum, cur.export(t).Checksum, "nothing was written")
}

func TestAutoImportUpgrade_NoRecordNoMergeBase_ExplainedRefusal(t *testing.T) {
	r := newUpgradeRig(t, releasedMtix(t, tag053, tag053Commit))
	a := r.clone()
	r.startOldProject(a)
	b := r.clone()
	r.old(b, "list")
	r.teammateUpgrades(b, "PROJ-2")
	r.pull(a)
	// A directory that is not a git checkout: copy the project out of git.
	solo := copyProject(t, a)
	cur := upgrade(t, solo)
	before := cur.export(t)

	err := cur.svc.AutoImport(context.Background(), cur.mtixDir)

	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	assert.True(t, mentions(cur.notices, "exchange record", "merge-base", "mtix import .mtix/tasks.json --mode merge"),
		cur.notices.String())
	assert.Equal(t, before.Checksum, cur.export(t).Checksum, "nothing was written")
}

func TestAutoImportUpgrade_BothChanged_ConflictListed(t *testing.T) {
	r := newUpgradeRig(t, releasedMtix(t, tag053, tag053Commit))
	a := r.clone()
	r.startOldProject(a)
	b := r.clone()
	r.old(b, "list")
	r.old(a, "claim", "PROJ-2", "--agent", "carol") // A's own claim of the task B claims
	r.discardBoard(a)
	r.teammateUpgrades(b, "PROJ-2")
	r.pull(a)
	cur := upgrade(t, a)
	before := cur.export(t)

	err := cur.svc.AutoImport(context.Background(), cur.mtixDir)

	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	assert.True(t, mentions(cur.notices, "PROJ-2", "changed both here and in the file", "assignee"), cur.notices.String())
	assert.Equal(t, before.Checksum, cur.export(t).Checksum)
}

func TestAutoImportUpgrade_053LargerStore_KeepsDeletedTaskAndHistory(t *testing.T) {
	r := newUpgradeRig(t, releasedMtix(t, tag053, tag053Commit))
	a := r.clone()
	r.old(a, "init", "--prefix", "PROJ")
	for _, title := range []string{"Task one", "Task two", "Task three", "Task four"} {
		r.old(a, "create", title)
	}
	r.old(a, "claim", "PROJ-1", "--agent", "alice")
	r.old(a, "claim", "PROJ-3", "--agent", "alice")
	r.old(a, "comment", "PROJ-3", "alice's note on task three")
	r.old(a, "dep", "add", "PROJ-2", "PROJ-1")
	r.old(a, "delete", "PROJ-4")
	r.publish(a, "larger board from the released mtix")
	b := r.clone()
	r.old(b, "list")
	r.teammateUpgrades(b, "PROJ-2")
	r.pull(a)
	cur := upgrade(t, a)
	before := cur.export(t)

	require.NoError(t, cur.svc.AutoImport(context.Background(), cur.mtixDir), cur.notices.String())

	after := cur.export(t)
	require.Len(t, after.Nodes, len(before.Nodes))
	for i := range after.Nodes {
		b, n := &before.Nodes[i], &after.Nodes[i]
		require.Equal(t, b.ID, n.ID)
		assert.Equal(t, b.DeletedAt != "", n.DeletedAt != "", "%s stays deleted or live", n.ID)
		assert.Len(t, n.Annotations, len(b.Annotations), "%s annotations", n.ID)
		assert.GreaterOrEqual(t, len(n.Activity), len(b.Activity), "%s activity", n.ID)
		assert.Equal(t, b.DeletedBy, n.DeletedBy, "%s deleted_by", n.ID)
	}
	assert.Equal(t, before.Dependencies, after.Dependencies)
	st2, as2, _, _ := nodeOf(t, after, "PROJ-2")
	assert.Equal(t, "in_progress", st2)
	assert.Equal(t, "bob", as2)
}

// A record in the current form is the last exchange: the older-form rule does
// not apply, the merge-base board is not consulted, and a lossy pull is refused
// as it was before this change.
func TestAutoImportUpgrade_CurrentFormRecord_KeepsTodaysRefusal(t *testing.T) {
	r := newUpgradeRig(t, releasedMtix(t, tag053, tag053Commit))
	a := r.setupSequence()
	r.pull(a)
	first := upgrade(t, a)
	require.NoError(t, first.svc.AutoImport(context.Background(), first.mtixDir), first.notices.String())
	rec, err := first.svc.LastExchanged(context.Background(), first.mtixDir)
	require.NoError(t, err)
	require.Equal(t, "current", rec.BoardForm, "the 2.0.0 board just imported is the last exchange")
	require.NoError(t, first.store.Close())

	// The teammate reopens PROJ-2 from a store that never held A's comment; the merge-base
	// board (A's original 1.x commit) would settle the pull if it were consulted.
	b := r.clone()
	teammate := upgrade(t, b)
	require.NoError(t, teammate.svc.AutoImport(context.Background(), teammate.mtixDir))
	require.NoError(t, teammate.store.UnclaimNode(context.Background(), "PROJ-2", "released", "bob"))
	require.NoError(t, teammate.svc.AutoExport(context.Background(), teammate.mtixDir))
	r.publish(b, "teammate reopens PROJ-2")
	r.pull(a)
	second := upgrade(t, a)
	before := second.export(t)

	err = second.svc.AutoImport(context.Background(), second.mtixDir)

	require.ErrorIs(t, err, service.ErrAutoImportRefused, second.notices.String())
	assert.False(t, mentions(second.notices, "told your changes"), second.notices.String())
	assert.Equal(t, before.Checksum, second.export(t).Checksum, "nothing was written")
}

// A dependency the store removed since the exchange must not come back from the file:
// the base holds no dependencies, so the difference is refused, as it was before.
func TestAutoImportUpgrade_LocalDependencyRemoval_IsRefusedNotRestored(t *testing.T) {
	r := newUpgradeRig(t, releasedMtix(t, tag053, tag053Commit))
	a := r.clone()
	r.startOldProject(a)
	r.old(a, "dep", "add", "PROJ-2", "PROJ-1")
	r.publish(a, "board with a dependency")
	b := r.clone()
	r.old(b, "list")
	r.old(a, "dep", "remove", "PROJ-2", "PROJ-1") // never pushed
	r.discardBoard(a)
	r.teammateUpgrades(b, "PROJ-2")
	r.pull(a)
	cur := upgrade(t, a)
	before := cur.export(t)
	require.Empty(t, before.Dependencies, "the released version removed the dependency from the store")

	err := cur.svc.AutoImport(context.Background(), cur.mtixDir)

	require.ErrorIs(t, err, service.ErrAutoImportRefused, cur.notices.String())
	assert.True(t, mentions(cur.notices, "PROJ-2", "dependency"), cur.notices.String())
	assert.Empty(t, cur.export(t).Dependencies, "the dependency was not restored")
}
