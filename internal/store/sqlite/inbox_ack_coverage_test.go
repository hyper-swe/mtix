// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	sqliteDriver "modernc.org/sqlite"
)

func TestInboxAckCoverage_FailedWriteRollsBackEffect(t *testing.T) {
	for _, tc := range []struct {
		name, table, operation, context string
		through                         bool
	}{
		{"selective insert", "agent_inbox_ack", "INSERT", "record selective inbox acknowledgement", false},
		{"cursor insert", "agent_inbox_cursor", "INSERT", "advance cumulative inbox acknowledgement", true},
		{"selective prune", "agent_inbox_ack", "DELETE", "prune selectively acknowledged prefix", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := budgetDataStore(t)
			first, _, last := addressThree(t, s, "worker")
			require.NoError(t, s.InboxAck(t.Context(), "worker", first))
			before, err := s.WakeAckMark(t.Context(), "worker")
			require.NoError(t, err)
			statement := "CREATE TRIGGER reject_ack BEFORE INSERT ON agent_inbox_ack BEGIN SELECT RAISE(ABORT,'ack fixture'); END"
			if tc.table == "agent_inbox_cursor" {
				statement = "CREATE TRIGGER reject_ack BEFORE INSERT ON agent_inbox_cursor BEGIN SELECT RAISE(ABORT,'cursor fixture'); END"
			}
			if tc.operation == "DELETE" {
				statement = "CREATE TRIGGER reject_ack BEFORE DELETE ON agent_inbox_ack BEGIN SELECT RAISE(ABORT,'prune fixture'); END"
			}
			_, err = s.WriteDB().ExecContext(t.Context(), statement)
			require.NoError(t, err)
			_, _, err = s.InboxAckCoverage(t.Context(), "worker", last, tc.through)
			require.Error(t, err)
			require.ErrorContains(t, err, tc.context)
			var cause *sqliteDriver.Error
			require.ErrorAs(t, err, &cause)
			after, err := s.WakeAckMark(t.Context(), "worker")
			require.NoError(t, err)
			require.Equal(t, before, after, "neither cursor nor selective effect may survive a failed transaction")
		})
	}
}

func TestInboxAckCoverage_ReadFailureNeverPublishesEffect(t *testing.T) {
	for _, name := range []string{"empty agent", "missing journal", "missing coverage"} {
		t.Run(name, func(t *testing.T) {
			s, _ := budgetDataStore(t)
			_, _, last := addressThree(t, s, "worker")
			agent := "worker"
			switch name {
			case "empty agent":
				agent = ""
			case "missing journal":
				_, err := s.WriteDB().ExecContext(t.Context(), "DROP TABLE sync_events")
				require.NoError(t, err)
			case "missing coverage":
				_, err := s.WriteDB().ExecContext(t.Context(), "DROP TABLE agent_inbox_ack")
				require.NoError(t, err)
			}
			before, after, err := s.InboxAckCoverage(t.Context(), agent, last, false)
			require.Error(t, err)
			require.Equal(t, before, after)
		})
	}
}

// Coverage counts recipient events once across direct and hook delivery routes.
func TestInboxAckCoverage_ThroughCountsOnlyNewRecipientEvents(t *testing.T) {
	s, _ := budgetDataStore(t)
	first, middle, last := int64(2), int64(3), int64(4)
	for _, event := range []struct{ id, op, payload string }{
		{"foreign", "create_node", `{}`},
		{"first", "comment", `{"to":"worker"}`},
		{"middle", "comment", `{"to":"worker"}`},
		{"last", "comment", `{"to":"worker"}`},
	} {
		_, err := s.WriteDB().ExecContext(t.Context(), `INSERT INTO sync_events
   (event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
   VALUES (?,'TEST','TEST-1',?,?,0,1,'{}','sender','test','2000-01-01T00:00:00Z')`, event.id, event.op, event.payload)
		require.NoError(t, err)
	}
	require.NoError(t, s.RecordInboxDelivery(t.Context(), "worker", first, "test-hook"))
	require.NoError(t, s.RecordInboxDelivery(t.Context(), "hook-worker", middle, "test-hook"))
	before, after, err := s.InboxAckCoverage(t.Context(), "foreign", first, false)
	require.NoError(t, err)
	require.Equal(t, int64(0), before)
	require.Equal(t, before, after, "a foreign selective acknowledgement has no recipient effect")
	require.NoError(t, s.InboxAck(t.Context(), "worker", first))
	for _, tc := range []struct {
		agent         string
		seq           int64
		before, after int64
	}{
		{"worker", first, 1, 1},
		{"worker", middle, 1, 2},
		{"worker", middle, 2, 2},
		{"worker", last, 2, 3},
		{"foreign", last, 0, 0},
		{"hook-worker", middle, 0, 1},
		{"hook-worker", last, 1, 1},
	} {
		before, after, err := s.InboxAckCoverage(t.Context(), tc.agent, tc.seq, true)
		require.NoError(t, err)
		require.Equal(t, tc.before, before)
		require.Equal(t, tc.after, after)
	}
}
