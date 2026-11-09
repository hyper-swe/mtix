// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestImportReconcile_JournalCollision_RefusesBeforeBackup(t *testing.T) {
	source := newTestStore(t)
	_, second, _ := addressThree(t, source, "recipient")
	incoming := transportExport(t, source)
	dest, raw := retentionStore(t)
	_, err := dest.Import(t.Context(), incoming, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE sync_events SET author_id='different' WHERE event_id=?`, incoming.InboxJournal.Events[0].EventID)
	require.NoError(t, err)
	require.NoError(t, source.InboxAck(t.Context(), "recipient", second))
	incoming = transportExport(t, source)
	backups := 0
	_, _, err = dest.ImportReconcile(t.Context(), incoming, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, BeforeWrite: func() error { backups++; return nil }})
	require.ErrorIs(t, err, model.ErrConflict)
	require.Zero(t, backups, "collision must be refused in the dry run, before external backup effects")
}

func TestImport_NoncommentIdentityCollision_RejectsWithoutBoardWrites(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	incoming := transportExport(t, source)
	dest, raw := retentionStore(t)
	_, err := dest.Import(t.Context(), incoming, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE sync_events SET op_type='create_node' WHERE event_id=?`, incoming.InboxJournal.Events[0].EventID)
	require.NoError(t, err)
	before, err := dest.Export(t.Context(), "", "")
	require.NoError(t, err)
	_, err = dest.Import(t.Context(), incoming, sqlite.ImportModeMerge, false)
	require.ErrorIs(t, err, model.ErrConflict)
	after, err := dest.Export(t.Context(), "", "")
	require.NoError(t, err)
	require.Equal(t, before.Checksum, after.Checksum)
}

func TestImport_AbsentJournalUID_StoresNull(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	_, err := raw.Exec(`UPDATE sync_events SET uid=NULL WHERE op_type='comment'`)
	require.NoError(t, err)
	dest, db := retentionStore(t)
	_, err = dest.Import(t.Context(), transportExport(t, source), sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	var absent int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM sync_events WHERE op_type='comment' AND uid IS NULL`).Scan(&absent))
	require.Equal(t, 3, absent)
}

func TestImport_ReplicaBookkeepingDrift_DoesNotCreateContentCollision(t *testing.T) {
	for _, statement := range []string{
		`UPDATE sync_events SET node_id='PROJ-99' WHERE event_id=?`,
		`UPDATE sync_events SET uid='adopted-uid' WHERE event_id=?`,
		`UPDATE sync_events SET created_at='2026-12-01T00:00:00Z' WHERE event_id=?`,
	} {
		t.Run(statement, func(t *testing.T) {
			source := newTestStore(t)
			addressThree(t, source, "recipient")
			incoming := transportExport(t, source)
			dest, raw := retentionStore(t)
			_, err := dest.Import(t.Context(), incoming, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			_, err = raw.Exec(statement, incoming.InboxJournal.Events[0].EventID)
			require.NoError(t, err)
			_, err = dest.Import(t.Context(), incoming, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			mail, err := dest.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			require.Len(t, mail, 3)
		})
	}
}

func TestImport_DistinctLargePayloadNumbers_RejectsIdentityCollision(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	_, err := raw.Exec(`UPDATE sync_events SET payload=json_set(payload,'$.opaque',json('9007199254740992')) WHERE op_type='comment'`)
	require.NoError(t, err)
	incoming := transportExport(t, source)
	dest, db := retentionStore(t)
	_, err = dest.Import(t.Context(), incoming, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE sync_events SET payload=json_set(payload,'$.opaque',json('9007199254740993')) WHERE event_id=?`, incoming.InboxJournal.Events[0].EventID)
	require.NoError(t, err)
	_, err = dest.Import(t.Context(), incoming, sqlite.ImportModeMerge, false)
	require.ErrorIs(t, err, model.ErrConflict, "different integer values must never compare equal after floating-point rounding")
}
