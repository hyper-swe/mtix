// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-107.31.7: a relay publisher writes its push cursor only after the
// records are on the medium. If the first publish crashes, or the cursor
// advance fails, readers can already hold the records while the store still
// reads as never synced (no cursor row, every event pending). The automatic
// import must still treat a store whose project configures a relay or a hub
// as synced, so a replace that gives a task another uid does not discard the
// pending events readers hold.

// relayConfigured is the local evidence the command layer passes: a relay
// directory is configured.
func relayConfigured() (service.HubEvidence, error) {
	return service.HubEvidence{ConfigKeys: true}, nil
}

func pendingEventCount(t *testing.T, f *guardFixture, node string) int {
	t.Helper()
	var n int
	require.NoError(t, f.store.WriteDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM sync_events WHERE node_id = ? AND sync_status = 'pending'`, node).Scan(&n))
	return n
}

func TestAutoImport_RelayConfiguredFirstPublishUnrecorded_RefusesAndDiscardsNothing(t *testing.T) {
	tests := []struct {
		name string
		// evidence is what the command layer reports; nil leaves the source unset.
		evidence func() (service.HubEvidence, error)
		// refused is whether the replace must be refused.
		refused bool
	}{
		{"relay configured, first publish's cursor advance failed", relayConfigured, true},
		{"nothing configured, nothing published", func() (service.HubEvidence, error) { return service.HubEvidence{}, nil }, false},
		{"no evidence source installed", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newGuardFixture(t)
			if tt.evidence != nil {
				f.svc.SetHubEvidenceSource(tt.evidence)
			}
			require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
			createTask(t, f, "PROJ-3", 3)
			require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
			// The records landed on the medium; the push cursor was never written.
			var cursors int
			require.NoError(t, f.store.WriteDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM relay_push_cursor`).Scan(&cursors))
			require.Zero(t, cursors)
			before := pendingEventCount(t, f, "PROJ-3")
			require.NotZero(t, before)
			created, err := f.store.GetNode(ctx, "PROJ-3")
			require.NoError(t, err)

			// A teammate's board holds PROJ-3 without its uid: the replace would
			// give the task another uid and discard its pending events.
			f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { setUID(d, "PROJ-3", "") }))
			err = f.svc.AutoImport(ctx, f.mtixDir)

			if tt.refused {
				require.ErrorIs(t, err, sqlite.ErrReplaceOnSyncedStore)
				assert.Equal(t, before, pendingEventCount(t, f, "PROJ-3"), "nothing readers may hold is discarded")
				var held int
				require.NoError(t, f.store.WriteDB().QueryRowContext(ctx,
					`SELECT COUNT(*) FROM nodes WHERE uid = ?`, created.UID).Scan(&held))
				assert.Equal(t, 1, held, "the task keeps its uid")
				return
			}
			require.NoError(t, err)
			assert.Zero(t, pendingEventCount(t, f, "PROJ-3"), "a store that shared nothing discards the dropped task's pending events")
		})
	}
}

// The evidence the command layer reports cannot be read: the automatic import
// fails closed, importing nothing, and wraps the cause.
func TestAutoImport_HubEvidenceUnreadable_FailsClosed(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.svc.SetHubEvidenceSource(relayConfigured)
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))
	// Without the board flags table the mark can be neither read nor written.
	_, err := f.store.WriteDB().ExecContext(ctx, `DROP TABLE board_flags`)
	require.NoError(t, err)

	err = f.svc.AutoImport(ctx, f.mtixDir)
	require.Error(t, err)
	assert.False(t, errors.Is(err, service.ErrAutoImportRefused))
	assert.Contains(t, err.Error(), "hub evidence")
	_, getErr := f.store.GetNode(ctx, "PROJ-3")
	assert.Error(t, getErr, "nothing was imported")
}

// MTIX-107.31.8: an exported hub DSN is transient evidence. The automatic
// import refuses a replace that would discard events while it is set, and
// writes no sticky mark, so a project that merely has the variable exported
// is not trapped once it is unset.
func TestAutoImport_ExportedDSN_RefusesWhileSetAndWritesNoMark(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	exported := true
	f.svc.SetHubEvidenceSource(func() (service.HubEvidence, error) {
		return service.HubEvidence{EnvDSNExported: exported}, nil
	})
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	createTask(t, f, "PROJ-3", 3)
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	before := pendingEventCount(t, f, "PROJ-3")
	require.NotZero(t, before)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { setUID(d, "PROJ-3", "") }))

	err := f.svc.AutoImport(ctx, f.mtixDir)
	require.ErrorIs(t, err, sqlite.ErrReplaceOnSyncedStore, "an exported DSN refuses the replace")
	assert.Equal(t, before, pendingEventCount(t, f, "PROJ-3"), "nothing readers may hold is discarded")
	mark, markErr := f.store.HubMark(ctx)
	require.NoError(t, markErr)
	assert.False(t, mark.Set, "an exported DSN never writes the sticky mark")

	exported = false // the variable is unset: the same file imports
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	assert.Zero(t, pendingEventCount(t, f, "PROJ-3"), "a store that shared nothing discards the dropped task's events")
}

// Durable evidence still records the mark, and exported evidence beside it
// does not change that.
func TestAutoImport_DurableEvidence_RecordsTheMarkEvenWithAnExportedDSN(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.svc.SetHubEvidenceSource(func() (service.HubEvidence, error) {
		return service.HubEvidence{ConfigKeys: true, EnvDSNExported: true}, nil
	})
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))
	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	mark, err := f.store.HubMark(ctx)
	require.NoError(t, err)
	assert.True(t, mark.Set)
}

// Evidence that cannot be read stops the automatic import: nothing is
// imported, and the cause is kept in the error chain.
func TestAutoImport_HubEvidenceSourceFails_StopsAndKeepsTheCause(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	cause := errors.New("secrets file unreadable")
	f.svc.SetHubEvidenceSource(func() (service.HubEvidence, error) { return service.HubEvidence{}, cause })
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))

	err := f.svc.AutoImport(ctx, f.mtixDir)
	require.ErrorIs(t, err, cause)
	assert.Contains(t, err.Error(), "read the hub evidence")
	_, getErr := f.store.GetNode(ctx, "PROJ-3")
	assert.Error(t, getErr, "nothing was imported")
}

// While an exported DSN is set, a lossy refusal of the automatic import is
// the synced one: it names the rebuild from the hub instead of a replace
// import, and the record keeps that the store was treated as synced.
func TestAutoImport_ExportedDSN_LossyRefusalIsTheSyncedOne(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.svc.SetHubEvidenceSource(func() (service.HubEvidence, error) {
		return service.HubEvidence{EnvDSNExported: true}, nil
	})
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil }))

	require.ErrorIs(t, f.svc.AutoImport(ctx, f.mtixDir), service.ErrAutoImportRefused)
	assertNoReplaceOffer(t, f.notices.String(), "the lossy refusal under an exported DSN")
	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.True(t, report.AutoImport.LastRefusal.Synced)
	mark, markErr := f.store.HubMark(ctx)
	require.NoError(t, markErr)
	assert.False(t, mark.Set, "and no mark is written")
}

// A DSN the command layer reports as refused (a DSN key in a tracked config, a
// secrets file with the wrong mode) is transient like an exported one: the
// automatic import refuses a replace that would discard events while it is
// reported, and writes no mark.
func TestAutoImport_RefusedDSN_RefusesAndWritesNoMark(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.svc.SetHubEvidenceSource(func() (service.HubEvidence, error) {
		return service.HubEvidence{DSNRefused: true}, nil
	})
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	createTask(t, f, "PROJ-3", 3)
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	before := pendingEventCount(t, f, "PROJ-3")
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { setUID(d, "PROJ-3", "") }))

	require.ErrorIs(t, f.svc.AutoImport(ctx, f.mtixDir), sqlite.ErrReplaceOnSyncedStore)
	assert.Equal(t, before, pendingEventCount(t, f, "PROJ-3"))
	mark, err := f.store.HubMark(ctx)
	require.NoError(t, err)
	assert.False(t, mark.Set)
}

// When part of the evidence cannot be read, the automatic import stops, but
// the durable part that was read (a config key) is still recorded as the
// mark, as before this change; the cause stays in the error chain.
func TestAutoImport_HubEvidencePartiallyRead_StopsAndRecordsTheDurablePart(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	cause := errors.New("secrets file unreadable")
	f.svc.SetHubEvidenceSource(func() (service.HubEvidence, error) {
		return service.HubEvidence{ConfigKeys: true}, cause
	})
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))

	err := f.svc.AutoImport(ctx, f.mtixDir)
	require.ErrorIs(t, err, cause)
	_, getErr := f.store.GetNode(ctx, "PROJ-3")
	assert.Error(t, getErr, "nothing was imported")
	mark, markErr := f.store.HubMark(ctx)
	require.NoError(t, markErr)
	assert.True(t, mark.Set, "the durable evidence already read is recorded")
	assert.Equal(t, "config", mark.Source)
}
