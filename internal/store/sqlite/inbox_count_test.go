// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Counts preserve journal eligibility, acknowledgements and read-only state.
package sqlite

import (
	"context"
	"database/sql"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

type inboxCounter interface {
	InboxCount(context.Context, string) (int64, int64, error)
}

func countBackend(t *testing.T, s *Store) inboxCounter {
	t.Helper()
	counter, ok := any(s).(inboxCounter)
	require.True(t, ok, "store must provide read-only inbox counts")
	return counter
}

func seedCountEvent(t *testing.T, s *Store, seq int64, op, payload string) {
	t.Helper()
	_, err := s.WriteDB().ExecContext(t.Context(), `INSERT INTO sync_events
 (rowid,event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
 VALUES (?,?,?,?,?,?,0,?,'{}','sender','test','2000-01-01T00:00:00Z')`, seq, "count-"+strconv.FormatInt(seq, 10), "TEST", "TEST-1", op, payload, seq)
	require.NoError(t, err)
}

func seedCountDelivery(t *testing.T, s *Store, seq int64) {
	t.Helper()
	_, err := s.WriteDB().ExecContext(t.Context(), `INSERT INTO inbox_deliveries(agent_id,event_seq,hook_name,delivered_at) VALUES (?,?,?,?)`, "w", seq, "test", "old")
	require.NoError(t, err)
}

func TestInboxCount_EligibilityAndAcknowledgements_ReturnsExactCounts(t *testing.T) {
	for _, tt := range []struct {
		name           string
		ack, through   int64
		pending, total int64
	}{
		{"unhandled", 0, 0, 4, 4}, {"selective", 3, 0, 3, 4}, {"through", 0, 2, 2, 4}, {"combined", 4, 2, 1, 4}, {"all acked", 0, 5, 0, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newDurabilityTestStore(t)
			counter := countBackend(t, s)
			seedCountEvent(t, s, 1, "comment", `{"to":"w"}`)
			seedCountEvent(t, s, 2, "comment", `{"to":"w"}`)
			seedCountEvent(t, s, 3, "update_field", `{}`)
			seedCountEvent(t, s, 4, "comment", `not json`)
			seedCountEvent(t, s, 5, "comment", `{"to":"other"}`)
			seedCountEvent(t, s, 6, "comment", `broken`)
			seedCountEvent(t, s, 7, "comment", `{"to":null}`)
			for _, seq := range []int64{2, 3, 4, 999} {
				seedCountDelivery(t, s, seq)
			}
			if tt.ack > 0 {
				require.NoError(t, s.InboxAck(t.Context(), "w", tt.ack))
			}
			if tt.through > 0 {
				require.NoError(t, s.InboxAckThrough(t.Context(), "w", tt.through))
			}
			pending, total, err := counter.InboxCount(t.Context(), "w")
			require.NoError(t, err)
			require.Equal(t, tt.pending, pending)
			require.Equal(t, tt.total, total)
			list, err := s.InboxList(t.Context(), "w")
			require.NoError(t, err)
			require.Len(t, list, int(pending))
			for _, agent := range []string{"never", "w' OR 1=1 --"} {
				p, n, e := counter.InboxCount(t.Context(), agent)
				require.NoError(t, e)
				require.Zero(t, p)
				require.Zero(t, n)
			}
			p, n, e := counter.InboxCount(t.Context(), "other")
			require.NoError(t, e)
			require.Equal(t, int64(1), p)
			require.Equal(t, int64(1), n)
		})
	}
}

func TestInboxCount_TouchContext_DoesNotMutateState(t *testing.T) {
	s, key := registrationFixture(t)
	counter := countBackend(t, s)
	seedCountEvent(t, s, 1, "comment", `{"to":"w"}`)
	seedCountEvent(t, s, 2, "comment", `{"to":"w"}`)
	require.NoError(t, s.InboxAck(t.Context(), "w", 2))
	require.NoError(t, s.InboxAckThrough(t.Context(), "w", 1))
	before, err := s.ListRegistrations(t.Context(), key.HostID, key.UID)
	require.NoError(t, err)
	ctx := WithSessionTouch(t.Context(), SessionTouch{HostID: "h", UID: "1", Handle: "s"})
	for range 2 {
		p, n, e := counter.InboxCount(ctx, "w")
		require.NoError(t, e)
		require.Zero(t, p)
		require.Equal(t, int64(2), n)
	}
	after, err := s.ListRegistrations(t.Context(), key.HostID, key.UID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	var cursor, acks int64
	require.NoError(t, s.ReadDB().QueryRowContext(t.Context(), `SELECT cursor FROM agent_inbox_cursor WHERE agent_id=?`, "w").Scan(&cursor))
	require.NoError(t, s.ReadDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM agent_inbox_ack WHERE agent_id=?`, "w").Scan(&acks))
	require.Equal(t, int64(1), cursor)
	require.Equal(t, int64(1), acks)
}

func TestInboxCount_InvalidOrUnavailable_ReturnsError(t *testing.T) {
	for _, name := range []string{"empty", "cancelled", "closed"} {
		t.Run(name, func(t *testing.T) {
			s, _ := newDurabilityTestStore(t)
			counter := countBackend(t, s)
			ctx := t.Context()
			agent := "w"
			if name == "empty" {
				agent = ""
			}
			if name == "cancelled" {
				c, cancel := context.WithCancel(ctx)
				cancel()
				ctx = c
			}
			if name == "closed" {
				require.NoError(t, s.Close())
			}
			_, _, err := counter.InboxCount(ctx, agent)
			require.Error(t, err)
			if name == "empty" {
				require.ErrorIs(t, err, model.ErrInvalidInput)
			}
			if name == "cancelled" {
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

func TestInboxCount_AddresseeIndex_IsPresentAndGuarded(t *testing.T) {
	s, _ := newDurabilityTestStore(t)
	var definition string
	err := s.ReadDB().QueryRowContext(t.Context(), `SELECT sql FROM sqlite_schema WHERE name=?`, "idx_sync_events_comment_addressee").Scan(&definition)
	require.NoError(t, err)
	require.Contains(t, definition, "CASE WHEN json_valid(payload)")
	seedCountEvent(t, s, 1, "comment", "broken")
}

func TestInboxCount_ConcurrentAtomicChanges_ReturnsCoherentSnapshot(t *testing.T) {
	s, _ := newDurabilityTestStore(t)
	counter := countBackend(t, s)
	seedCountEvent(t, s, 1, "comment", `{"to":"w"}`)
	done := make(chan error, 1)
	go func() {
		for seq := int64(2); seq <= 30; seq++ {
			err := s.WithTx(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(t.Context(), `INSERT INTO sync_events
 (rowid,event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
 VALUES (?,?,?,?,'comment',?,0,?,'{}','sender','test','old')`, seq, "parallel-"+strconv.FormatInt(seq, 10), "TEST", "TEST-1", `{"to":"w"}`, seq)
				if err != nil {
					return err
				}
				_, err = tx.ExecContext(t.Context(), `INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES (?,?) ON CONFLICT(agent_id) DO UPDATE SET cursor=excluded.cursor`, "w", seq-seq%2)
				return err
			})
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for range 100 {
		p, n, err := counter.InboxCount(t.Context(), "w")
		require.NoError(t, err)
		require.Equal(t, n%2, p)
	}
	require.NoError(t, <-done)
}

func TestInboxCount_SingleAckedEvent_ReturnsZeroPendingOneTotal(t *testing.T) {
	s, _ := newDurabilityTestStore(t)
	counter := countBackend(t, s)
	seedCountEvent(t, s, 1, "comment", `{"to":"w"}`)
	require.NoError(t, s.InboxAck(t.Context(), "w", 1))
	p, n, err := counter.InboxCount(t.Context(), "w")
	require.NoError(t, err)
	require.Zero(t, p)
	require.Equal(t, int64(1), n)
}
