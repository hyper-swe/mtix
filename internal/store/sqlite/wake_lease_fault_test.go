// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Fault fixtures verify transaction failure precedence and retained lease state.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func internalWakeKey() WakeLeaseKey {
	return WakeLeaseKey{AgentID: "TEST-agent", HostID: "TEST-host", UID: "1000"}
}

func internalWakeLease(t *testing.T, s *Store) WakeLease {
	t.Helper()
	lease, err := s.AcquireWakeLease(t.Context(), internalWakeKey(), "", time.Minute)
	require.NoError(t, err)
	return lease
}

func TestAcquireWakeLease_EntropyUnavailable_PreservesAbsentState(t *testing.T) {
	s := newInternalTestStore(t)
	_, err := s.acquireWakeLease(t.Context(), internalWakeKey(), "", time.Minute, strings.NewReader(""))
	require.ErrorIs(t, err, io.EOF)
	var count int
	require.NoError(t, s.readDB.QueryRow("SELECT COUNT(*) FROM wake_leases").Scan(&count))
	assert.Zero(t, count)
}

func TestWakeLease_DamagedTimestamps_ReturnsCauseWithoutMutation(t *testing.T) {
	for _, column := range []string{"acquired_at", "expires_at"} {
		t.Run(column, func(t *testing.T) {
			s := newInternalTestStore(t)
			lease := internalWakeLease(t, s)
			_, err := s.writeDB.Exec("UPDATE wake_leases SET " + column + "='unreadable'")
			require.NoError(t, err)
			_, err = s.AcquireWakeLease(t.Context(), internalWakeKey(), "", time.Minute)
			require.ErrorContains(t, err, column)
			assert.NotErrorIs(t, err, model.ErrAlreadyClaimed)
			_, err = s.ReleaseWakeLease(t.Context(), internalWakeKey(), lease.Holder, time.Minute, false)
			require.ErrorContains(t, err, column)
			var value string
			require.NoError(t, s.readDB.QueryRow("SELECT "+column+" FROM wake_leases").Scan(&value))
			assert.Equal(t, "unreadable", value)
		})
	}
}

func TestWakeLease_MissingTable_ReturnsDatabaseError(t *testing.T) {
	s := newInternalTestStore(t)
	lease := internalWakeLease(t, s)
	_, err := s.writeDB.Exec("DROP TABLE wake_leases")
	require.NoError(t, err)
	_, err = s.AcquireWakeLease(t.Context(), internalWakeKey(), "", time.Minute)
	require.ErrorContains(t, err, "wake_leases")
	_, err = s.ReleaseWakeLease(t.Context(), internalWakeKey(), lease.Holder, time.Minute, false)
	require.ErrorContains(t, err, "wake_leases")
}

func TestWakeLease_AbortedWrites_KeepOriginalOwner(t *testing.T) {
	for _, operation := range []string{"insert", "dirty", "renew", "delete"} {
		t.Run(operation, func(t *testing.T) {
			s := newInternalTestStore(t)
			var lease WakeLease
			if operation != "insert" {
				lease = internalWakeLease(t, s)
			}
			event := "UPDATE"
			if operation == "insert" {
				event = "INSERT"
			}
			if operation == "delete" {
				event = "DELETE"
			}
			_, err := s.writeDB.Exec("CREATE TRIGGER stop_wake BEFORE " + event + " ON wake_leases BEGIN SELECT RAISE(ABORT,'owned write fault'); END")
			require.NoError(t, err)
			switch operation {
			case "insert", "dirty":
				_, err = s.AcquireWakeLease(t.Context(), internalWakeKey(), "", time.Minute)
			case "renew":
				_, err = s.AcquireWakeLease(t.Context(), internalWakeKey(), lease.Holder, time.Minute)
			case "delete":
				_, err = s.ReleaseWakeLease(t.Context(), internalWakeKey(), lease.Holder, time.Minute, true)
			}
			require.ErrorContains(t, err, "owned write fault")
			assert.NotErrorIs(t, err, model.ErrAlreadyClaimed)
			var count int
			require.NoError(t, s.readDB.QueryRow("SELECT COUNT(*) FROM wake_leases").Scan(&count))
			if operation == "insert" {
				assert.Zero(t, count)
			} else {
				assert.Equal(t, 1, count)
				require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error {
					row, err := readWakeLease(t.Context(), tx, internalWakeKey())
					require.NoError(t, err)
					assert.Equal(t, lease, row.WakeLease)
					assert.False(t, row.dirty)
					return nil
				}))
			}
		})
	}
}

func TestAcquireWakeLease_CommitRejected_ReturnsFailureBeforeHeld(t *testing.T) {
	s := newInternalTestStore(t)
	lease := internalWakeLease(t, s)
	_, err := s.writeDB.Exec(`CREATE TABLE wake_parent(id INTEGER PRIMARY KEY);
 CREATE TABLE wake_child(id INTEGER REFERENCES wake_parent(id) DEFERRABLE INITIALLY DEFERRED);
 CREATE TRIGGER reject_wake_commit AFTER UPDATE ON wake_leases BEGIN INSERT INTO wake_child VALUES(1); END;`)
	require.NoError(t, err)
	_, err = s.AcquireWakeLease(t.Context(), internalWakeKey(), "", time.Minute)
	require.Error(t, err)
	assert.NotErrorIs(t, err, model.ErrAlreadyClaimed)
	assert.Contains(t, err.Error(), "commit transaction")
	var holder string
	var dirty int
	require.NoError(t, s.readDB.QueryRow("SELECT holder,dirty FROM wake_leases").Scan(&holder, &dirty))
	assert.Equal(t, lease.Holder, holder)
	assert.Zero(t, dirty)
}

func TestWakeLease_CanceledContext_ReturnsCancellation(t *testing.T) {
	s := newInternalTestStore(t)
	lease := internalWakeLease(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := s.AcquireWakeLease(ctx, internalWakeKey(), "", time.Minute)
	assert.ErrorIs(t, err, context.Canceled)
	_, err = s.ReleaseWakeLease(ctx, internalWakeKey(), lease.Holder, time.Minute, false)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestReleaseWakeLease_InvalidIdentityAndToken_RefusesInput(t *testing.T) {
	s := newInternalTestStore(t)
	for _, key := range []WakeLeaseKey{{}, internalWakeKey()} {
		_, err := s.ReleaseWakeLease(t.Context(), key, "", time.Minute, false)
		require.ErrorIs(t, err, model.ErrInvalidInput)
	}
}

func TestWakeLease_JournalUnavailable_DoesNotCreateOrDeleteLease(t *testing.T) {
	s := newInternalTestStore(t)
	lease := internalWakeLease(t, s)
	_, err := s.writeDB.Exec("DROP TABLE sync_events")
	require.NoError(t, err)
	key := internalWakeKey()
	key.AgentID = "other"
	_, err = s.AcquireWakeLease(t.Context(), key, "", time.Minute)
	require.ErrorContains(t, err, "sync_events")
	_, err = s.ReleaseWakeLease(t.Context(), internalWakeKey(), lease.Holder, time.Minute, false)
	require.ErrorContains(t, err, "sync_events")
	err = s.WithTx(t.Context(), func(tx *sql.Tx) error {
		_, err := rearmWakeLease(t.Context(), tx, internalWakeKey(), ReleaseResult{}, time.Now())
		return err
	})
	require.Error(t, err)
	var count int
	require.NoError(t, s.readDB.QueryRow("SELECT COUNT(*) FROM wake_leases").Scan(&count))
	assert.Equal(t, 1, count)
	assert.False(t, errors.Is(err, model.ErrAlreadyClaimed))
}
