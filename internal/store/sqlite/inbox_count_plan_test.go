// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Rule 1b pins the production constant's bounded access plan and safe upgrade.
package sqlite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInboxCount_QueryPlan_UsesAddresseeAndDeliveryIndexes(t *testing.T) {
	s, _ := newDurabilityTestStore(t)
	for seq := int64(1); seq <= 100; seq++ {
		seedCountEvent(t, s, seq, "comment", `{"to":"other"}`)
	}
	seedCountEvent(t, s, 101, "comment", `{"to":"w"}`)
	seedCountDelivery(t, s, 101)
	// SQL rule 1b permits only this fixed prefix and our own source constant.
	rows, err := s.ReadDB().QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+inboxCountSQL, "w", "w", "w", "w")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())
	plan := strings.Join(details, "\n")
	require.Contains(t, plan, "SEARCH e USING INDEX idx_sync_events_comment_addressee")
	require.Contains(t, plan, "SEARCH d USING COVERING INDEX sqlite_autoindex_inbox_deliveries_1")
	require.Contains(t, plan, "SEARCH e USING INTEGER PRIMARY KEY")
	require.Contains(t, plan, "sqlite_autoindex_agent_inbox_cursor_1")
	require.Contains(t, plan, "sqlite_autoindex_agent_inbox_ack_1")
	require.NotContains(t, plan, "SCAN e")
	require.NotContains(t, plan, "idx_sync_events_op_type")
}

func TestInboxCount_IndexUpgrade_MalformedRowsRemainInsertable(t *testing.T) {
	s, path := newDurabilityTestStore(t)
	_, err := s.WriteDB().ExecContext(t.Context(), `DROP INDEX idx_sync_events_comment_addressee`)
	require.NoError(t, err)
	seedCountEvent(t, s, 1, "comment", "invalid legacy JSON")
	var version string
	require.NoError(t, s.ReadDB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='schema_version'`).Scan(&version))
	require.NoError(t, s.Close())
	for range 2 {
		reopened, err := New(path, s.logger)
		require.NoError(t, err)
		seedCountEvent(t, reopened, 2, "comment", "broken")
		_, err = reopened.WriteDB().ExecContext(t.Context(), `DELETE FROM sync_events WHERE rowid=?`, 2)
		require.NoError(t, err)
		var after string
		require.NoError(t, reopened.ReadDB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='schema_version'`).Scan(&after))
		require.Equal(t, version, after)
		p, n, err := reopened.InboxCount(t.Context(), "w")
		require.NoError(t, err)
		require.Zero(t, p)
		require.Zero(t, n)
		require.NoError(t, reopened.Close())
	}
	// No new mailbox table or version-changing migration is introduced.
	_, err = os.Stat(filepath.Dir(path))
	require.NoError(t, err)
}

func TestInboxCount_IndexCreationError_ReturnsWrappedError(t *testing.T) {
	s, _ := newDurabilityTestStore(t)
	_, err := s.WriteDB().ExecContext(t.Context(), `DROP INDEX idx_sync_events_comment_addressee`)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(t.Context(), `CREATE TABLE idx_sync_events_comment_addressee (id INTEGER)`)
	require.NoError(t, err)
	require.ErrorContains(t, s.ensureInboxCountIndex(t.Context()), "create inbox addressee index")
	require.NoError(t, s.Close())
	require.Error(t, s.ensureInboxCountIndex(t.Context()))
}
