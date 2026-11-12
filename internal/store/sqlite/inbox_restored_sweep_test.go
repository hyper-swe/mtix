// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSalvageInbox_CancelledContext_ReturnsCancellation(t *testing.T) {
	s := exchangeSnapshotStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := salvageInboxRows(ctx, s.readDB.rawDB, &RecoverResult{})
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, result.Events)
}

func TestRestoredInbox_SweepAndExchangeDoNotCountMailAsHeld(t *testing.T) {
	s := exchangeSnapshotStore(t)
	for _, id := range []string{"restored-mail", "real-mail"} {
		_, err := s.WriteDB().Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,sync_status,created_at) VALUES(?,'TEST','TEST-1','comment','{"to":"worker","body":"mail"}',0,0,'{}','sender','','applied','2026-11-05T00:00:00Z')`, id)
		require.NoError(t, err)
	}
	_, err := s.WriteDB().Exec(`INSERT INTO inbox_restored_events VALUES('restored-mail')`)
	require.NoError(t, err)
	ids := []string{"restored-mail", "real-mail", "unknown-mail"}
	missing, err := s.MissingLocalEventIDs(t.Context(), ids)
	require.NoError(t, err)
	require.Equal(t, []string{"restored-mail", "unknown-mail"}, missing)
	require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error {
		n, err := countUnresolvedExchange(t.Context(), tx, ids)
		require.NoError(t, err)
		require.Equal(t, int64(2), n)
		return nil
	}))
	_, err = s.WriteDB().Exec(`DROP TABLE inbox_restored_events`)
	require.NoError(t, err)
	missing, err = s.MissingLocalEventIDs(t.Context(), ids)
	require.NoError(t, err)
	require.Equal(t, []string{"unknown-mail"}, missing)
	require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error {
		n, err := countUnresolvedExchange(t.Context(), tx, ids)
		require.NoError(t, err)
		require.Equal(t, int64(1), n)
		return nil
	}))
}
