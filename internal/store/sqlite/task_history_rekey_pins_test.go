// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func putEntry(t *testing.T, raw *sql.DB, entry, key, field, event string) {
	t.Helper()
	_, err := raw.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, event_id)
		VALUES (?, ?, '2026-10-13T09:00:00.000000000Z', 'a', 'hub', 'change', ?, ?)`, entry, key, field, event)
	require.NoError(t, err)
}

func fieldsUnder(t *testing.T, raw *sql.DB, key string) []string {
	t.Helper()
	rows, err := raw.Query(`SELECT field FROM task_history WHERE task_uid = ? ORDER BY field`, key)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []string
	for rows.Next() {
		var f string
		require.NoError(t, rows.Scan(&f))
		out = append(out, f)
	}
	require.NoError(t, rows.Err())
	return out
}

func rekeyTx(t *testing.T, s *Store, fn func(tx *sql.Tx) error) {
	t.Helper()
	require.NoError(t, s.WithTx(context.Background(), fn))
}

// TestRekeyHistoryExact_DuplicateIsOneEventAndFieldOnly: only the entry of the
// same event AND the same field is a duplicate; another field of that event
// is real history and must survive the move.
func TestRekeyHistoryExact_DuplicateIsOneEventAndFieldOnly(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	putEntry(t, raw, "e1", "OLD", "status", "EV1")
	putEntry(t, raw, "e2", "OLD", "priority", "EV1")
	putEntry(t, raw, "e3", "NEW", "status", "EV1")
	rekeyTx(t, s, func(tx *sql.Tx) error { return rekeyHistoryExact(context.Background(), tx, "OLD", "NEW") })
	assert.Equal(t, []string{"priority", "status"}, fieldsUnder(t, raw, "NEW"))
	assert.Empty(t, fieldsUnder(t, raw, "OLD"))
}

func TestRekeyHistoryExact_SameKey_ChangesNothing(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	putEntry(t, raw, "e1", "K", "status", "EV1")
	putEntry(t, raw, "e2", "K", "priority", "EV1")
	rekeyTx(t, s, func(tx *sql.Tx) error { return rekeyHistoryExact(context.Background(), tx, "K", "K") })
	assert.Equal(t, []string{"priority", "status"}, fieldsUnder(t, raw, "K"))
}

// TestRekeyHistorySubtree_DuplicateOnADescendant_IsDroppedAndTheRestMoves:
// the dedupe covers descendants, not just the root, and keeps other fields.
func TestRekeyHistorySubtree_DuplicateOnADescendant_IsDroppedAndTheRestMoves(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	putEntry(t, raw, "e1", "P-1.1", "status", "EV1")
	putEntry(t, raw, "e2", "P-1.1", "priority", "EV1")
	putEntry(t, raw, "e3", "P-5.1", "status", "EV1")
	putEntry(t, raw, "e4", "P-1", "title", "EV2")
	rekeyTx(t, s, func(tx *sql.Tx) error { return rekeyHistorySubtree(context.Background(), tx, "P-1", "P-5") })
	assert.Equal(t, []string{"priority", "status"}, fieldsUnder(t, raw, "P-5.1"))
	assert.Equal(t, []string{"title"}, fieldsUnder(t, raw, "P-5"))
	assert.Empty(t, fieldsUnder(t, raw, "P-1.1"))
}
