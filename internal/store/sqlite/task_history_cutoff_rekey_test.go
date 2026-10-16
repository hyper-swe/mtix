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

func putCutoff(t *testing.T, raw *sql.DB, key, cutoff string) {
	t.Helper()
	_, err := raw.Exec(`INSERT INTO history_retention_cutoff (task_uid, cutoff_ts, updated_at) VALUES (?, ?, ?)`, key, cutoff, cutoff)
	require.NoError(t, err)
}

func cutoffsByKey(t *testing.T, raw *sql.DB) map[string]string {
	t.Helper()
	rows, err := raw.Query(`SELECT task_uid, cutoff_ts FROM history_retention_cutoff`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	out := map[string]string{}
	for rows.Next() {
		var k, c string
		require.NoError(t, rows.Scan(&k, &c))
		out[k] = c
	}
	require.NoError(t, rows.Err())
	return out
}

// A task's cutoff moves with its history when the history is rekeyed, so the
// task keeps its resurrection protection under the new key; on a collision the
// newer cutoff wins.
func TestRekeyHistoryExact_MovesTheCutoffWithTheHistory(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	putCutoff(t, raw, "OLD-1", "2026-10-13T09:00:00.000000000Z")
	putCutoff(t, raw, "NEW-1", "2026-10-13T08:00:00.000000000Z")
	putCutoff(t, raw, "OLD-2", "2026-10-13T07:00:00.000000000Z")
	rekeyTx(t, s, func(tx *sql.Tx) error { return rekeyHistoryExact(context.Background(), tx, "OLD-1", "NEW-1") })
	rekeyTx(t, s, func(tx *sql.Tx) error { return rekeyHistoryExact(context.Background(), tx, "OLD-2", "NEW-2") })
	assert.Equal(t, map[string]string{"NEW-1": "2026-10-13T09:00:00.000000000Z", "NEW-2": "2026-10-13T07:00:00.000000000Z"}, cutoffsByKey(t, raw))
}

func TestRekeyHistorySubtree_MovesTheCutoffsOfTheWholeSubtree(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	putCutoff(t, raw, "P-1", "2026-10-13T09:00:00.000000000Z")
	putCutoff(t, raw, "P-1.2", "2026-10-13T08:00:00.000000000Z")
	putCutoff(t, raw, "P-10", "2026-10-13T07:00:00.000000000Z") // not in the subtree
	rekeyTx(t, s, func(tx *sql.Tx) error { return rekeyHistorySubtree(context.Background(), tx, "P-1", "P-5") })
	assert.Equal(t, map[string]string{"P-5": "2026-10-13T09:00:00.000000000Z", "P-5.2": "2026-10-13T08:00:00.000000000Z",
		"P-10": "2026-10-13T07:00:00.000000000Z"}, cutoffsByKey(t, raw))
}

// On a collision the newer cutoff wins in the other direction too: the one
// already held under the new key is newer than the one being moved.
func TestRekeyHistoryExact_CollisionWithANewerCutoffUnderTheNewKey_KeepsIt(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	putCutoff(t, raw, "OLD-1", "2026-10-13T07:00:00.000000000Z")
	putCutoff(t, raw, "NEW-1", "2026-10-13T09:00:00.000000000Z")
	rekeyTx(t, s, func(tx *sql.Tx) error { return rekeyHistoryExact(context.Background(), tx, "OLD-1", "NEW-1") })
	assert.Equal(t, map[string]string{"NEW-1": "2026-10-13T09:00:00.000000000Z"}, cutoffsByKey(t, raw))
}

func TestRekeyHistorySubtree_CollisionKeepsTheNewerCutoffInBothDirections(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	putCutoff(t, raw, "P-1", "2026-10-13T07:00:00.000000000Z") // older than the one under P-5
	putCutoff(t, raw, "P-5", "2026-10-13T09:00:00.000000000Z")
	putCutoff(t, raw, "P-1.2", "2026-10-13T09:30:00.000000000Z") // newer than the one under P-5.2
	putCutoff(t, raw, "P-5.2", "2026-10-13T08:00:00.000000000Z")
	rekeyTx(t, s, func(tx *sql.Tx) error { return rekeyHistorySubtree(context.Background(), tx, "P-1", "P-5") })
	assert.Equal(t, map[string]string{"P-5": "2026-10-13T09:00:00.000000000Z", "P-5.2": "2026-10-13T09:30:00.000000000Z"}, cutoffsByKey(t, raw))
}
