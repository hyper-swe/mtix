// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollectSalvageInboxRows_DuplicateUnaddressedIdentity_IsIncomplete(t *testing.T) {
	db := routeSeekDB(t, true)
	for _, query := range []string{
		`PRAGMA foreign_keys=OFF`,
		`CREATE TABLE copied_events AS SELECT * FROM sync_events`,
		`DROP TABLE sync_events`,
		`ALTER TABLE copied_events RENAME TO sync_events`,
		`UPDATE sync_events SET event_id='duplicate',payload='{"body":"unaddressed"}'`,
	} {
		_, err := db.Exec(query)
		require.NoError(t, err)
	}
	rows, err := collectSalvageInboxRows(t.Context(), db)
	require.Len(t, rows, 2, "retain readable rows while reporting the damage signal")
	require.ErrorContains(t, err, "duplicate journal identity")
	require.ErrorContains(t, err, "lost record count unknown")
}

func TestRestoreSalvageCoverage_DuplicateIdentity_IsIncomplete(t *testing.T) {
	db := routeSeekDB(t, true)
	walk := inboxRowidWalk{next: 1}
	row, _, ok := walk.read(t.Context(), db)
	require.True(t, ok)
	journal := &InboxJournal{Version: 1}
	result := &RecoverResult{}
	restoreSalvageCoverage(t.Context(), db, []salvageMail{row, row}, journal, result)
	require.Len(t, journal.Events, 1)
	require.True(t, result.InboxIncomplete, "combining repeated identity is a damage signal, not proof of completeness")
}
