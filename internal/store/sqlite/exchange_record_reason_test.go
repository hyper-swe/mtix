// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Fixed-key receipt bookkeeping preserves durability guards and board isolation.
package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestSaveExchangeRecordReason_UpdateClearAndReopen_DoNotNotifyBoard(t *testing.T) {
	for _, value := range []string{"publish-failed", "unknown", ""} {
		t.Run("value="+value, func(t *testing.T) {
			s, path := newDurabilityTestStore(t)
			clock := func() time.Time { return time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC) }
			s.SetClock(clock)
			before, err := s.Export(t.Context(), "TEST", "reference")
			require.NoError(t, err)
			tail, err := s.JournalTail(t.Context())
			require.NoError(t, err)
			notifications := 0
			s.SetOnCommit(func() { notifications++ })
			require.NoError(t, s.SaveExchangeRecordReason(t.Context(), "partial-pull"))
			require.NoError(t, s.SaveExchangeRecordReason(t.Context(), value))
			require.Zero(t, notifications)
			after, err := s.Export(t.Context(), "TEST", "reference")
			require.NoError(t, err)
			require.Equal(t, before, after)
			afterTail, err := s.JournalTail(t.Context())
			require.NoError(t, err)
			require.Equal(t, tail, afterTail)
			require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error {
				_, err := tx.ExecContext(t.Context(), `INSERT INTO meta(key,value) VALUES ('notification-control','ordinary')`)
				return err
			}))
			require.Equal(t, 1, notifications)
			require.NoError(t, s.Close())
			reopened, err := New(path, slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			var stored string
			require.NoError(t, reopened.QueryRow(t.Context(), `SELECT value FROM meta WHERE key='meta.sync.exchange_record.reason'`).Scan(&stored))
			require.Equal(t, value, stored)
		})
	}
}

func TestSaveExchangeRecordReason_ConfiguredFloor_RefusesBeforePersistence(t *testing.T) {
	t.Setenv(minFreeBytesEnv, "18446744073709551615")
	s, _ := newDurabilityTestStore(t)
	notifications := 0
	s.SetOnCommit(func() { notifications++ })
	err := s.SaveExchangeRecordReason(t.Context(), "partial-pull")
	require.ErrorIs(t, err, model.ErrDiskFull)
	var stored string
	err = s.QueryRow(t.Context(), `SELECT value FROM meta WHERE key='meta.sync.exchange_record.reason'`).Scan(&stored)
	require.ErrorIs(t, err, sql.ErrNoRows)
	require.Zero(t, notifications)
}

func TestSaveExchangeRecordReason_ContextClosedAndMissingMeta_ReturnErrors(t *testing.T) {
	for _, kind := range []string{"context", "closed", "missing meta"} {
		t.Run(kind, func(t *testing.T) {
			s, path := newDurabilityTestStore(t)
			require.NoError(t, s.SaveExchangeRecordReason(t.Context(), "publish-failed"))
			ctx := t.Context()
			switch kind {
			case "context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "closed":
				require.NoError(t, s.Close())
			case "missing meta":
				_, err := s.WriteDB().ExecContext(ctx, `DROP TABLE meta`)
				require.NoError(t, err)
			}
			notifications := 0
			s.SetOnCommit(func() { notifications++ })
			err := s.SaveExchangeRecordReason(ctx, "partial-pull")
			require.ErrorContains(t, err, "save exchange record reason")
			require.Zero(t, notifications)
			if kind == "context" {
				require.ErrorIs(t, err, context.Canceled)
			}
			if kind == "missing meta" {
				require.ErrorContains(t, err, "no such table: meta")
				return
			}
			require.NoError(t, s.Close())
			reopened, err := New(path, slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			var stored string
			require.NoError(t, reopened.QueryRow(t.Context(), `SELECT value FROM meta WHERE key='meta.sync.exchange_record.reason'`).Scan(&stored))
			require.Equal(t, "publish-failed", stored)
		})
	}
}
