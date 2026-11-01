// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func newQuarantineAckStore(t *testing.T) (*Store, QuarantinedEvent, QuarantineAcknowledgement) {
	t.Helper()
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	q := QuarantinedEvent{EventID: "held", Source: "pull", RawEvent: `{"event_id":"held","node_id":"TEST-1é"}`, Reason: "invalid node id"}
	require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error { return QuarantineEvent(t.Context(), tx, q) }))
	sum := sha256.Sum256([]byte(q.RawEvent))
	a := QuarantineAcknowledgement{EventID: q.EventID, RawEventSHA256: hex.EncodeToString(sum[:]), Source: q.Source, Actor: "operator", Reason: "reviewed", AcknowledgedAt: "2026-03-14T12:00:00Z"}
	return s, q, a
}

func quarantineAckHistoryCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	require.NoError(t, s.QueryRow(t.Context(), "SELECT COUNT(*) FROM sync_quarantine_acknowledgements").Scan(&n))
	return n
}

func TestQuarantineAcknowledgement_StaleSnapshot_AtomicRefusal(t *testing.T) {
	for _, changed := range []string{"source", "raw", "removed"} {
		t.Run(changed, func(t *testing.T) {
			s, q, a := newQuarantineAckStore(t)
			stmt := map[string]string{
				"source":  `UPDATE sync_quarantine SET source = 'push'`,
				"raw":     `UPDATE sync_quarantine SET raw_event = raw_event || ' '`,
				"removed": `DELETE FROM sync_quarantine`,
			}[changed]
			_, err := s.WriteDB().ExecContext(t.Context(), stmt)
			require.NoError(t, err)
			_, err = s.RecordQuarantineAcknowledgement(t.Context(), q, a)
			require.Error(t, err)
			if changed == "removed" {
				require.ErrorIs(t, err, model.ErrNotFound)
			} else {
				require.ErrorIs(t, err, model.ErrInvalidInput)
			}
			require.Zero(t, quarantineAckHistoryCount(t, s))
		})
	}
}

func TestQuarantineAcknowledgement_ImmutableAndLocalOnly(t *testing.T) {
	s, q, a := newQuarantineAckStore(t)
	notifications := 0
	s.SetOnCommit(func() { notifications++ })
	got, err := s.RecordQuarantineAcknowledgement(t.Context(), q, a)
	require.NoError(t, err)
	require.Equal(t, a, got.QuarantineAcknowledgement)
	require.False(t, got.AlreadyAcknowledged)
	require.Zero(t, notifications, "local receipts must not schedule exports or task hooks")
	for _, stmt := range []string{
		`UPDATE sync_quarantine_acknowledgements SET reason = 'replaced'`,
		`DELETE FROM sync_quarantine_acknowledgements`,
	} {
		_, err := s.WriteDB().ExecContext(t.Context(), stmt)
		require.ErrorContains(t, err, "immutable")
	}
	counts, err := s.CountQuarantineAcknowledgements(t.Context())
	require.NoError(t, err)
	require.Equal(t, QuarantineCounts{Total: 1, Acknowledged: 1}, counts)
	q.Source = "push"
	ack, err := s.QuarantineAcknowledgementFor(t.Context(), q)
	require.NoError(t, err)
	require.Nil(t, ack, "a historical pull receipt never acknowledges a push hold")
}

func TestQuarantineAcknowledgement_ConcurrentRepeats_FirstReceiptStable(t *testing.T) {
	s, q, first := newQuarantineAckStore(t)
	_, err := s.RecordQuarantineAcknowledgement(t.Context(), q, first)
	require.NoError(t, err)
	var wg sync.WaitGroup
	type result struct {
		receipt QuarantineAcknowledgementResult
		err     error
	}
	results := make(chan result, 8)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := first
			a.Actor, a.Reason, a.AcknowledgedAt = "later", "changed", "2026-04-01T00:00:00Z"
			got, err := s.RecordQuarantineAcknowledgement(t.Context(), q, a)
			results <- result{got, err}
		}()
	}
	wg.Wait()
	close(results)
	for r := range results {
		require.NoError(t, r.err)
		require.Equal(t, first, r.receipt.QuarantineAcknowledgement)
		require.True(t, r.receipt.AlreadyAcknowledged)
	}
	require.Equal(t, 1, quarantineAckHistoryCount(t, s))
}

func TestQuarantineAcknowledgement_Reopen_AdditiveV4AndReceiptSurvives(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			s, q, first := newQuarantineAckStore(t)
			_, err := s.RecordQuarantineAcknowledgement(t.Context(), q, first)
			require.NoError(t, err)
			if legacy {
				_, err = s.WriteDB().ExecContext(t.Context(), "DROP TABLE sync_quarantine_acknowledgements")
				require.NoError(t, err)
			}
			dir := s.dbDir
			require.NoError(t, s.Close())
			reopened, err := New(dir, slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { _ = reopened.Close() })
			var version string
			require.NoError(t, reopened.QueryRow(t.Context(), "SELECT value FROM meta WHERE key = 'schema_version'").Scan(&version))
			require.Equal(t, "4", version)
			ack, err := reopened.QuarantineAcknowledgementFor(t.Context(), q)
			require.NoError(t, err)
			if legacy {
				require.Nil(t, ack)
			} else {
				require.Equal(t, &first, ack)
			}
			row, err := reopened.QuarantineForAcknowledgement(t.Context(), q.EventID)
			require.NoError(t, err)
			require.Equal(t, q.RawEvent, row.RawEvent)
		})
	}
}

func TestQuarantineAcknowledgement_ReadOnlyLegacy_NoFalsePassAndWriteRefused(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			s, q, a := newQuarantineAckStore(t)
			if legacy {
				_, err := s.WriteDB().ExecContext(t.Context(), "DROP TABLE sync_quarantine_acknowledgements")
				require.NoError(t, err)
			}
			path := s.dbPath
			require.NoError(t, s.Close())
			ro, err := openReadOnlyStore(t.Context(), path, slog.Default(), defaultOpenRetry(), "macfuse")
			require.NoError(t, err)
			t.Cleanup(func() { _ = ro.Close() })
			counts, err := ro.CountQuarantineAcknowledgements(t.Context())
			require.NoError(t, err)
			require.Equal(t, QuarantineCounts{Total: 1, Unacknowledged: 1}, counts)
			ack, err := ro.QuarantineAcknowledgementFor(t.Context(), q)
			require.NoError(t, err)
			require.Nil(t, ack)
			_, err = ro.RecordQuarantineAcknowledgement(t.Context(), q, a)
			require.ErrorIs(t, err, errUnsafeFilesystem)
			_, err = ro.QuarantineForAcknowledgement(t.Context(), "missing")
			require.Error(t, err)
		})
	}
}

func TestQuarantineAcknowledgement_DistinctRawHistory_OneCurrentCount(t *testing.T) {
	s, q, a := newQuarantineAckStore(t)
	_, err := s.RecordQuarantineAcknowledgement(t.Context(), q, a)
	require.NoError(t, err)
	_, err = s.WriteDB().ExecContext(t.Context(), `UPDATE sync_quarantine SET raw_event = raw_event || ' '`)
	require.NoError(t, err)
	q.RawEvent += " "
	counts, err := s.CountQuarantineAcknowledgements(t.Context())
	require.NoError(t, err)
	require.Equal(t, QuarantineCounts{Total: 1, Unacknowledged: 1}, counts)
	sum := sha256.Sum256([]byte(q.RawEvent))
	a.RawEventSHA256 = hex.EncodeToString(sum[:])
	_, err = s.RecordQuarantineAcknowledgement(t.Context(), q, a)
	require.NoError(t, err)
	counts, err = s.CountQuarantineAcknowledgements(t.Context())
	require.NoError(t, err)
	require.Equal(t, QuarantineCounts{Total: 1, Acknowledged: 1}, counts)
	require.Equal(t, 2, quarantineAckHistoryCount(t, s))
}

func TestQuarantineAcknowledgement_ClosedAuditReads_FailClosed(t *testing.T) {
	for _, readonly := range []bool{false, true} {
		t.Run(map[bool]string{false: "writable", true: "read-only"}[readonly], func(t *testing.T) {
			s, q, _ := newQuarantineAckStore(t)
			require.NoError(t, s.Close())
			s.writeRefused = readonly
			_, err := s.CountQuarantineAcknowledgements(t.Context())
			require.Error(t, err)
			ack, err := s.QuarantineAcknowledgementFor(t.Context(), q)
			require.Error(t, err)
			require.Nil(t, ack)
			_, err = s.QuarantineForAcknowledgement(t.Context(), q.EventID)
			require.Error(t, err)
		})
	}
}

func TestQuarantineAcknowledgement_AuditInsertFailure_Atomic(t *testing.T) {
	s, q, a := newQuarantineAckStore(t)
	_, err := s.WriteDB().ExecContext(t.Context(), `CREATE TRIGGER deny_ack_insert
		BEFORE INSERT ON sync_quarantine_acknowledgements
		BEGIN SELECT RAISE(ABORT, 'owned audit failure'); END`)
	require.NoError(t, err)
	_, err = s.RecordQuarantineAcknowledgement(t.Context(), q, a)
	require.ErrorContains(t, err, "owned audit failure")
	require.Zero(t, quarantineAckHistoryCount(t, s))
	row, err := s.QuarantineForAcknowledgement(t.Context(), q.EventID)
	require.NoError(t, err)
	require.Equal(t, q.RawEvent, row.RawEvent)
}

// Concurrent calls report exactly one newly persisted receipt (MTIX-187.2).
func TestQuarantineAcknowledgement_ConcurrentFirstCalls_ReportsOneNewReceipt(t *testing.T) {
	s, q, receipt := newQuarantineAckStore(t)
	type result struct {
		receipt any
		err     error
	}
	results := make(chan result, 8)
	var wg sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := s.RecordQuarantineAcknowledgement(t.Context(), q, receipt)
			results <- result{got, err}
		}()
	}
	wg.Wait()
	close(results)
	newlyRecorded := 0
	for r := range results {
		require.NoError(t, r.err)
		raw, err := json.Marshal(r.receipt)
		require.NoError(t, err)
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		require.Equal(t, receipt.Actor, body["actor"])
		require.Equal(t, receipt.Reason, body["reason"])
		existed, ok := body["already_acknowledged"].(bool)
		require.True(t, ok, "every call must distinguish a new receipt from an existing one")
		if !existed {
			newlyRecorded++
		}
	}
	require.Equal(t, 1, newlyRecorded)
	require.Equal(t, 1, quarantineAckHistoryCount(t, s))
}
