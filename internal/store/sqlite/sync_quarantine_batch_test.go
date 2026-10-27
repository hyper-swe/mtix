// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// Batch receipt tests preserve exact snapshot identity and readonly compatibility.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"log/slog"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestQuarantineAcknowledgementsFor_PageBounds(t *testing.T) {
	for _, size := range []int{0, 500, 501} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			s, q, a := newQuarantineAckStore(t)
			_, err := s.RecordQuarantineAcknowledgement(t.Context(), q, a)
			require.NoError(t, err)
			page := make([]QuarantinedEvent, size)
			for i := range page {
				page[i] = q
			}
			s.ReadDB().SetMaxIdleConns(0)
			before := s.ReadDB().Stats().MaxIdleClosed
			got, err := s.QuarantineAcknowledgementsFor(t.Context(), page)
			reads := s.ReadDB().Stats().MaxIdleClosed - before
			switch size {
			case 0:
				require.NoError(t, err)
				require.Empty(t, got)
				require.Zero(t, reads)
			case 500:
				require.NoError(t, err)
				require.Equal(t, map[string]QuarantineAcknowledgement{q.EventID: a}, got)
				require.Equal(t, int64(1), reads)
			case 501:
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.Nil(t, got)
				require.Zero(t, reads)
			}
		})
	}
}

func TestQuarantineAcknowledgementsFor_ExactBytesAndSource(t *testing.T) {
	for _, tc := range []struct{ name, id, raw, source string }{
		{"pull", "held", `{"event_id":"held"}`, "pull"},
		{"sweep and arbitrary bytes", "odd'\x00id\xff", "raw\x00\xff", "sweep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q, a := newQuarantineAckStore(t)
			q.EventID, q.RawEvent, q.Source = tc.id, tc.raw, tc.source
			_, err := s.WriteDB().ExecContext(t.Context(), "UPDATE sync_quarantine SET event_id = ?, raw_event = ?, source = ?", q.EventID, q.RawEvent, q.Source)
			require.NoError(t, err)
			a.EventID, a.Source = q.EventID, q.Source
			sum := sha256.Sum256([]byte(q.RawEvent))
			a.RawEventSHA256 = hex.EncodeToString(sum[:])
			_, err = s.RecordQuarantineAcknowledgement(t.Context(), q, a)
			require.NoError(t, err)
			got, err := s.QuarantineAcknowledgementsFor(t.Context(), []QuarantinedEvent{q})
			require.NoError(t, err)
			require.Equal(t, map[string]QuarantineAcknowledgement{q.EventID: a}, got)
			for _, change := range []string{"raw", "id", "source"} {
				t.Run(change, func(t *testing.T) {
					other := q
					switch change {
					case "raw":
						other.RawEvent += " "
					case "id":
						other.EventID += "missing"
					case "source":
						other.Source = "push"
					}
					got, err := s.QuarantineAcknowledgementsFor(t.Context(), []QuarantinedEvent{other})
					require.NoError(t, err)
					require.Empty(t, got)
				})
			}
		})
	}
}

func TestQuarantineAcknowledgementsFor_ReadOnlyLegacy(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "legacy"}[legacy], func(t *testing.T) {
			s, q, a := newQuarantineAckStore(t)
			_, err := s.RecordQuarantineAcknowledgement(t.Context(), q, a)
			require.NoError(t, err)
			if legacy {
				_, err = s.WriteDB().ExecContext(t.Context(), "DROP TABLE sync_quarantine_acknowledgements")
				require.NoError(t, err)
			}
			path := s.dbPath
			require.NoError(t, s.Close())
			ro, err := openReadOnlyStore(t.Context(), path, slog.Default(), defaultOpenRetry(), "macfuse")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, ro.Close()) })
			got, err := ro.QuarantineAcknowledgementsFor(t.Context(), []QuarantinedEvent{q})
			require.NoError(t, err)
			if legacy {
				require.Empty(t, got)
			} else {
				require.Equal(t, map[string]QuarantineAcknowledgement{q.EventID: a}, got)
			}
		})
	}
}

func TestQuarantineAcknowledgementsFor_QueryFailuresAreErrors(t *testing.T) {
	for _, kind := range []string{"missing table", "closed pool", "cancelled", "schema inspection"} {
		t.Run(kind, func(t *testing.T) {
			s, q, _ := newQuarantineAckStore(t)
			ctx := t.Context()
			switch kind {
			case "missing table":
				_, err := s.WriteDB().ExecContext(ctx, "DROP TABLE sync_quarantine_acknowledgements")
				require.NoError(t, err)
			case "closed pool":
				require.NoError(t, s.ReadDB().Close())
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "schema inspection":
				s.writeRefused = true
				require.NoError(t, s.ReadDB().Close())
			}
			got, err := s.QuarantineAcknowledgementsFor(ctx, []QuarantinedEvent{q})
			require.Error(t, err)
			require.Nil(t, got)
			if kind == "cancelled" {
				require.ErrorIs(t, err, context.Canceled)
			}
		})
	}
}

func TestQuarantineAcknowledgementsFor_CorruptAuditReadFails(t *testing.T) {
	for _, kind := range []string{"null field", "iteration error"} {
		t.Run(kind, func(t *testing.T) {
			s, q, a := newQuarantineAckStore(t)
			_, err := s.RecordQuarantineAcknowledgement(t.Context(), q, a)
			require.NoError(t, err)
			page := []QuarantinedEvent{q}
			if kind == "null field" {
				_, err = s.WriteDB().ExecContext(t.Context(), "DROP TABLE sync_quarantine_acknowledgements")
				require.NoError(t, err)
				_, err = s.WriteDB().ExecContext(t.Context(), `CREATE TABLE sync_quarantine_acknowledgements (event_id TEXT, raw_event TEXT, raw_event_sha256 TEXT, source TEXT, actor TEXT, reason TEXT, acknowledged_at TEXT)`)
				require.NoError(t, err)
				_, err = s.WriteDB().ExecContext(t.Context(), `INSERT INTO sync_quarantine_acknowledgements VALUES (?, ?, ?, ?, NULL, ?, ?)`, q.EventID, q.RawEvent, a.RawEventSHA256, a.Source, a.Reason, a.AcknowledgedAt)
				require.NoError(t, err)
			} else {
				second := q
				second.EventID = "later"
				second.RawEvent = strings.Replace(q.RawEvent, q.EventID, second.EventID, 1)
				require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error { return QuarantineEvent(t.Context(), tx, second) }))
				secondReceipt := a
				secondReceipt.EventID = second.EventID
				sum := sha256.Sum256([]byte(second.RawEvent))
				secondReceipt.RawEventSHA256 = hex.EncodeToString(sum[:])
				_, err = s.RecordQuarantineAcknowledgement(t.Context(), second, secondReceipt)
				require.NoError(t, err)
				page = append(page, second)
				_, err = s.WriteDB().ExecContext(t.Context(), "ALTER TABLE sync_quarantine_acknowledgements RENAME TO receipt_history")
				require.NoError(t, err)
				_, err = s.WriteDB().ExecContext(t.Context(), `CREATE VIEW sync_quarantine_acknowledgements AS SELECT event_id, raw_event, raw_event_sha256, source, CASE WHEN event_id = 'later' THEN abs(-9223372036854775808) ELSE actor END AS actor, reason, acknowledged_at FROM receipt_history`)
				require.NoError(t, err)
			}
			got, err := s.QuarantineAcknowledgementsFor(t.Context(), page)
			require.Error(t, err)
			require.Nil(t, got)
		})
	}
}
