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

func historyEntryKeys(t *testing.T, raw *sql.DB) map[string]string {
	t.Helper()
	rows, err := raw.Query(`SELECT entry_id, task_uid FROM task_history`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	out := map[string]string{}
	for rows.Next() {
		var e, k string
		require.NoError(t, rows.Scan(&e, &k))
		out[e] = k
	}
	require.NoError(t, rows.Err())
	return out
}

// A subtree whose ids hold a non-ASCII character rekeys its history, its
// duplicate check and its retention cutoffs by characters: a Go byte offset
// passed to SQLite SUBSTR cut the é in two and wrote AA-1é.51 for AA-1é.5.1.
func TestRekeyHistorySubtree_NonASCIIID_RekeysTheWholeSubtree(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	putEntry(t, raw, "e1", "AA-1é.4", "status", "EV1")
	putEntry(t, raw, "e2", "AA-1é.4.1", "status", "EV1")
	putEntry(t, raw, "e3", "AA-1é.4.21", "status", "EV1")
	// An entry already under the new key for the same event is a duplicate.
	putEntry(t, raw, "e4", "AA-1é.5.1", "status", "EV1")
	putCutoff(t, raw, "AA-1é.4.2", "2026-10-13T09:00:00.000000000Z")
	rekeyTx(t, s, func(tx *sql.Tx) error { return rekeyHistorySubtree(context.Background(), tx, "AA-1é.4", "AA-1é.5") })
	assert.Equal(t, map[string]string{"e1": "AA-1é.5", "e3": "AA-1é.5.21", "e4": "AA-1é.5.1"}, historyEntryKeys(t, raw))
	assert.Equal(t, map[string]string{"AA-1é.5.2": "2026-10-13T09:00:00.000000000Z"}, cutoffsByKey(t, raw))
}

func insertPendingEvent(t *testing.T, raw *sql.DB, id, nodeID, op, payload string) {
	t.Helper()
	_, err := raw.Exec(`INSERT INTO sync_events
		(event_id, project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock, vector_clock,
		 author_id, author_machine_hash, sync_status, created_at)
		VALUES (?, 'AA', ?, ?, ?, 1, 1, '{}', 'alice', '0123456789abcdef', 'pending', '2026-01-01T00:00:00Z')`,
		id, nodeID, op, payload)
	require.NoError(t, err)
}

// Pending events of a renumbered non-ASCII subtree are re-addressed by
// characters: the node id of an event without a uid, the parent of a child
// creation, and the target of a link.
func TestRequeueRenumberedEvents_NonASCIIID_ReAddressesEvents(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	insertPendingEvent(t, raw, "n1", "AA-1é.4.3", "update_field", `{}`)
	insertPendingEvent(t, raw, "c1", "AA-1é.5.2", "create_node", `{"parent_id":"AA-1é.4.1"}`)
	insertPendingEvent(t, raw, "l1", "AA-1é.9", "link_dep", `{"depends_on_node_id":"AA-1é.4.2"}`)
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		return requeueRenumberedEvents(context.Background(), tx, "gone", "AA-1é.4", "AA-1é.5")
	}))
	var node, payload string
	require.NoError(t, raw.QueryRow(`SELECT node_id FROM sync_events WHERE event_id = 'n1'`).Scan(&node))
	assert.Equal(t, "AA-1é.5.3", node)
	require.NoError(t, raw.QueryRow(`SELECT payload FROM sync_events WHERE event_id = 'c1'`).Scan(&payload))
	assert.JSONEq(t, `{"parent_id":"AA-1é.5.1"}`, payload)
	require.NoError(t, raw.QueryRow(`SELECT payload FROM sync_events WHERE event_id = 'l1'`).Scan(&payload))
	assert.JSONEq(t, `{"depends_on_node_id":"AA-1é.5.2"}`, payload)
}
