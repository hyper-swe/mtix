// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Regression tests for MTIX-95.31.11 (FR-15.2h): the conflict baseline
// (.mtix/data/sync-db.sha256) is the hash of the local store's export. 0.5.3
// hashed its 1.0.0 export and 0.5.4 hashes the 2.0.0 one, so the first
// automatic import after upgrading a store from 0.5.3 refused a teammate's
// board as "both the local store and tasks.json changed" although nothing
// changed. So did the first one after the open-time backfill (MTIX-95.31.9)
// gave uids to tasks that had none when the baseline was written. A
// baseline that matches the unchanged store in such an older form is now
// rewritten in the current form and is no conflict; a real local change is
// still one. Written red-first.
package service_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// baselineUpgradedEvent is the log event of a baseline rewritten from an
// older form, as the text log writes it, once per event.
const baselineUpgradedEvent = "event=sync_baseline_upgraded"

// writeBaseline writes hash as the conflict baseline, as the last export
// before the upgrade did.
func (f *guardFixture) writeBaseline(t *testing.T, hash string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(f.mtixDir, "data", "sync-db.sha256"), []byte(hash), 0o644))
}

// currentBaseline returns the conflict baseline this version writes for the
// store as it is: the hash of its export without the export time.
func (f *guardFixture) currentBaseline(t *testing.T) string {
	t.Helper()
	data, err := f.store.Export(context.Background(), "", "")
	require.NoError(t, err)
	data.ExportedAt = ""
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return hashBytes(raw)
}

// teammateChange is a teammate's change on the pulled board: PROJ-1
// retitled and PROJ-3 added.
func teammateChange(t *testing.T) func(d *sqlite.ExportData) {
	return func(d *sqlite.ExportData) {
		d.Nodes[nodeIndex(t, d, "PROJ-1")].Title = "Task PROJ-1, retitled by a teammate"
		addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3)
	}
}

// The versions whose conflict baseline a case writes (olderBaselineCase.by);
// "" is this version's, written by the fixture's export.
const (
	by053 = "0.5.3" // the 1.0.0 form, uids included (v053BaselineHash)
	by030 = "0.3.0" // the 1.0.0 form without any uid key (v030BaselineHash)
)

// olderBaselineCase is a store upgraded with its conflict baseline written
// in an older form, as the tests below build it.
type olderBaselineCase struct {
	name   string
	setup  upgradeSetup
	by     string // the version that wrote the baseline: by053, by030, or "" for this one
	pinned string // the baseline pinned for the setup, when there is one
	reopen bool   // the next command opens the store, minting backfill uids
	form   string // the older form the log names, as the text log writes it
}

// prepare builds the store, writes the baseline and opens the store again
// when the case says so.
func (c *olderBaselineCase) prepare(t *testing.T) *guardFixture {
	t.Helper()
	f := newUpgradeFixture(t, c.setup)
	baseline := ""
	switch c.by {
	case by053:
		baseline = v053BaselineHash(t, f.store)
	case by030:
		baseline = v030BaselineHash(t, f.store)
	}
	if baseline != "" {
		if c.pinned != "" {
			require.Equal(t, c.pinned, baseline, "the fixture is the pinned one")
		}
		f.writeBaseline(t, baseline)
	}
	if c.reopen {
		f.reopen(t)
	}
	return f
}

// TestAutoImport_BaselineInAnOlderForm_AppliesTeammateBoard verifies the
// first automatic import after the upgrade applies a teammate's board when
// the local store is unchanged since its baseline was written: by 0.5.3
// (1.0.0 form), or by this version before backfill uids were minted, or
// both, or by 0.3.0 (1.0.0 form without any uid key; the upgrade gave the
// tasks uids). The baseline is rewritten in the current form, logged once.
// The rows with invalid UTF-8 also pin the order in which the older forms
// are built: the uids are left out before the 1.0.0 checksum is computed.
func TestAutoImport_BaselineInAnOlderForm_AppliesTeammateBoard(t *testing.T) {
	tests := []olderBaselineCase{
		{name: "0.5.3 baseline", by: by053, pinned: v053FixtureBaseline, form: "1.0.0"},
		{name: "0.5.3 baseline, a text holding invalid UTF-8", setup: upgradeSetup{invalidUTF8: true},
			by: by053, pinned: v053FixtureBaselineInvalidUTF8, form: "1.0.0"},
		{name: "0.5.3 baseline of a store without tasks", setup: upgradeSetup{empty: true}, by: by053, form: "1.0.0"},
		{name: "baseline written before backfill uids were minted", setup: upgradeSetup{uidless: true}, reopen: true,
			form: `"current without backfill uids"`},
		{name: "0.5.3 baseline written before backfill uids were minted", setup: upgradeSetup{uidless: true},
			by: by053, reopen: true, form: `"1.0.0 without backfill uids"`},
		{name: "0.5.3 baseline written before backfill uids were minted, a text holding invalid UTF-8",
			setup: upgradeSetup{invalidUTF8: true, uidless: true}, by: by053, reopen: true,
			form: `"1.0.0 without backfill uids"`},
		{name: "0.3.0 baseline", by: by030, pinned: v030FixtureBaseline, form: `"1.0.0 without uids"`},
		{name: "0.3.0 baseline, a text holding invalid UTF-8", setup: upgradeSetup{invalidUTF8: true},
			by: by030, pinned: v030FixtureBaselineInvalidUTF8, form: `"1.0.0 without uids"`},
		{name: "0.3.0 baseline, a task given a backfill uid at the upgrade", setup: upgradeSetup{uidless: true},
			by: by030, reopen: true, form: `"1.0.0 without uids"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := tt.prepare(t)
			if tt.setup.empty {
				f.pull(t, newGuardFixture(t).teammateBoard(t, func(d *sqlite.ExportData) {
					addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3)
				}))
			} else {
				f.pull(t, f.teammateBoard(t, teammateChange(t)))
			}

			require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

			assert.Contains(t, f.notices.String(), "mtix: imported the changed .mtix/tasks.json", "log: %s", f.logs)
			_, err := f.store.GetNode(ctx, "PROJ-3")
			require.NoError(t, err, "the teammate's task is imported")
			report, err := f.svc.Compare(ctx, f.mtixDir)
			require.NoError(t, err)
			assert.Nil(t, report.AutoImport.LastRefusal, "no conflict is recorded")
			assert.Equal(t, 1, strings.Count(f.logs.String(), baselineUpgradedEvent), "the upgrade is logged once")
			assert.Contains(t, f.logs.String(), baselineUpgradedEvent+" from_form="+tt.form)
			assert.Equal(t, f.currentBaseline(t), string(f.read(t, "data/sync-db.sha256")))
		})
	}
}

// TestAutoImport_BaselineFrom053AndLossyBoard_RefusedAsLossyBaselineUpgraded
// verifies the baseline is rewritten in the current form when the import
// then stops for another reason: a board from a teammate still on 0.5.3
// lacks the local annotations, so it is refused as a loss, never as a
// conflict, and the next command does not rewrite the baseline again.
func TestAutoImport_BaselineFrom053AndLossyBoard_RefusedAsLossyBaselineUpgraded(t *testing.T) {
	ctx := context.Background()
	c := olderBaselineCase{by: by053, pinned: v053FixtureBaseline}
	f := c.prepare(t)
	unchanged := f.currentBaseline(t)
	f.pull(t, asOlderClientBoard(t, f.teammateBoard(t, teammateChange(t))))

	for command := 1; command <= 2; command++ {
		err := f.svc.AutoImport(ctx, f.mtixDir)
		require.ErrorIs(t, err, service.ErrAutoImportRefused, "command %d", command)
	}

	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.Equal(t, "lossy", report.AutoImport.LastRefusal.Kind, "the store did not change: no conflict")
	assert.Contains(t, f.notices.String(), "a replace import of the changed file would delete local data the file lacks")
	assert.NotContains(t, f.notices.String(), "both the local store and the file changed")
	assert.Equal(t, unchanged, string(f.read(t, "data/sync-db.sha256")), "the baseline is rewritten in the current form")
	assert.Equal(t, 1, strings.Count(f.logs.String(), baselineUpgradedEvent), "rewritten once")
	assert.Equal(t, 2, f.annotationCount(t, "PROJ-1"), "the local annotations are kept")
}

// TestAutoImport_LocalChangeNotExported_StillAConflict is the control: a
// local change that was never exported, made before the upgrade or after a
// baseline this version wrote, is still reported as a conflict, whatever
// form the baseline is in; the baseline is left as it is. A change only to
// a field 2.0.0 added (an annotation) is one too under a baseline this
// version wrote.
func TestAutoImport_LocalChangeNotExported_StillAConflict(t *testing.T) {
	retitle := func(t *testing.T, f *guardFixture) {
		f.exec(t, `UPDATE nodes SET title = 'Task PROJ-2, retitled locally' WHERE id = 'PROJ-2'`)
	}
	annotate := func(t *testing.T, f *guardFixture) {
		require.NoError(t, f.store.SetAnnotations(context.Background(), "PROJ-2", []model.Annotation{{
			ID: "01J9UPGRADE0000000000000001", Author: "dev", Text: "never exported", CreatedAt: f.now,
		}}))
	}
	tests := []struct {
		olderBaselineCase
		change func(t *testing.T, f *guardFixture)
	}{
		{olderBaselineCase{name: "0.5.3 baseline, a title changed", by: by053, pinned: v053FixtureBaseline}, retitle},
		{olderBaselineCase{name: "0.5.3 baseline written before backfill uids were minted, a title changed",
			setup: upgradeSetup{uidless: true}, by: by053, reopen: true}, retitle},
		{olderBaselineCase{name: "0.3.0 baseline, a title changed", by: by030, pinned: v030FixtureBaseline}, retitle},
		{olderBaselineCase{name: "baseline written before backfill uids were minted, a title changed",
			setup: upgradeSetup{uidless: true}, reopen: true}, retitle},
		{olderBaselineCase{name: "current baseline, a title changed"}, retitle},
		{olderBaselineCase{name: "current baseline, an annotation added"}, annotate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := tt.prepare(t)
			f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))
			tt.change(t, f)
			baseline := string(f.read(t, "data/sync-db.sha256"))

			err := f.svc.AutoImport(ctx, f.mtixDir)
			if err != nil {
				require.ErrorIs(t, err, service.ErrAutoImportRefused)
			}

			report, err := f.svc.Compare(ctx, f.mtixDir)
			require.NoError(t, err)
			require.NotNil(t, report.AutoImport.LastRefusal, "the conflict is recorded")
			assert.Equal(t, "conflict", report.AutoImport.LastRefusal.Kind)
			_, err = f.store.GetNode(ctx, "PROJ-3")
			assert.ErrorIs(t, err, model.ErrNotFound, "nothing is imported")
			assert.Equal(t, baseline, string(f.read(t, "data/sync-db.sha256")), "the baseline is left as it is")
			assert.NotContains(t, f.logs.String(), baselineUpgradedEvent)
		})
	}
}

// TestAutoImport_CurrentBaselineUnchangedStore_AppliesWithoutUpgrade verifies
// a baseline this version wrote is compared exactly as before: the store
// matches it, so the board applies and no older form is involved.
func TestAutoImport_CurrentBaselineUnchangedStore_AppliesWithoutUpgrade(t *testing.T) {
	ctx := context.Background()
	f := newUpgradeFixture(t, upgradeSetup{})
	require.Equal(t, f.currentBaseline(t), string(f.read(t, "data/sync-db.sha256")))
	f.pull(t, f.teammateBoard(t, teammateChange(t)))

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	_, err := f.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, err)
	assert.NotContains(t, f.logs.String(), baselineUpgradedEvent)
}

// TestAutoImport_BaselineRewriteFails_WarnsAndKeepsOlderBaseline verifies
// the rewrite of a baseline recognized in an older form is atomic: in a data
// directory mtix cannot write to, the rewrite fails with a warning, the
// older baseline stays as it was (never truncated), and no rewrite is
// logged. The next command recognizes it again.
func TestAutoImport_BaselineRewriteFails_WarnsAndKeepsOlderBaseline(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	ctx := context.Background()
	c := olderBaselineCase{by: by053, pinned: v053FixtureBaseline}
	f := c.prepare(t)
	f.pull(t, f.teammateBoard(t, teammateChange(t)))
	dataDir := filepath.Join(f.mtixDir, "data")
	require.NoError(t, os.Chmod(dataDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dataDir, 0o755) }) // runs before the store closes and the directory is removed

	// Nothing is imported either: the backup before the import cannot be
	// written, which is logged, not returned.
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))

	assert.Contains(t, f.logs.String(), "could not rewrite the conflict baseline in the current form")
	assert.NotContains(t, f.logs.String(), baselineUpgradedEvent)
	assert.Equal(t, v053FixtureBaseline, string(f.read(t, "data/sync-db.sha256")), "the older baseline is kept")
	_, err := f.store.GetNode(ctx, "PROJ-3")
	assert.ErrorIs(t, err, model.ErrNotFound)
}
