// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func exportRequestTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(filepath.Join(t.TempDir(), "mtix.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s
}

func exportRequestTime() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }

func TestExportRequest_MetadataDoesNotDispatchMutationHooks(t *testing.T) {
	s := exportRequestTestStore(t)
	ctx := context.Background()
	calls := 0
	s.SetOnCommit(func() { calls++ })
	require.NoError(t, s.MarkExportRequest(ctx, exportRequestTime()))
	cutoff, err := s.SnapshotExportRequest(ctx)
	require.NoError(t, err)
	require.NoError(t, s.RetireExportRequest(ctx, cutoff))
	require.Zero(t, calls, "bookkeeping must not recursively schedule a mirror/hook")
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES (?,?)", "owned-mutation", "value")
		return err
	}))
	require.Equal(t, 1, calls, "ordinary mutation callback behavior is unchanged")
}

func TestExportRequest_LateGenerationRetainsExactOldest(t *testing.T) {
	s := exportRequestTestStore(t)
	ctx := context.Background()
	first, late := exportRequestTime(), exportRequestTime().Add(time.Hour)
	require.NoError(t, s.MarkExportRequest(ctx, first))
	cutoff, err := s.SnapshotExportRequest(ctx)
	require.NoError(t, err)
	// A different native read pool sees the committed cutoff before export.
	state, err := s.ReadExportRequest(ctx)
	require.NoError(t, err)
	require.Equal(t, cutoff, *state.Active)
	require.NoError(t, s.MarkExportRequest(ctx, late))
	require.NoError(t, s.MarkExportRequest(ctx, late.Add(time.Minute)))
	require.NoError(t, s.RetireExportRequest(ctx, cutoff))
	state, err = s.ReadExportRequest(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), state.Requested)
	require.Equal(t, int64(1), state.Covered)
	require.Equal(t, late.Format(time.RFC3339), state.Oldest)
	require.Nil(t, state.Active)
	require.Empty(t, state.Late)
	next, err := s.SnapshotExportRequest(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, s.RetireExportRequest(ctx, cutoff), model.ErrConflict, "old holders cannot retire a replacement cutoff")
	require.NoError(t, s.RetireExportRequest(ctx, next))
	state, err = s.ReadExportRequest(ctx)
	require.NoError(t, err)
	require.Equal(t, state.Requested, state.Covered)
	require.Empty(t, state.Oldest)
}

func TestExportRequest_ActiveSnapshotRestartAndOverflow(t *testing.T) {
	s := exportRequestTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.MarkExportRequest(ctx, exportRequestTime()))
	_, err := s.SnapshotExportRequest(ctx)
	require.NoError(t, err)
	require.NoError(t, s.MarkExportRequest(ctx, exportRequestTime().Add(time.Hour)))
	next, err := s.SnapshotExportRequest(ctx) // Recovery replaces an abandoned active cutoff.
	require.NoError(t, err)
	require.Equal(t, int64(2), next)
	require.NoError(t, s.RetireExportRequest(ctx, next))
	seedExportRequest(t, s, ExportRequestState{Version: 1, Requested: math.MaxInt64, Covered: math.MaxInt64})
	require.ErrorIs(t, s.MarkExportRequest(ctx, exportRequestTime()), model.ErrConflict)
	state, err := s.ReadExportRequest(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(math.MaxInt64), state.Requested, "generation never wraps or resets")
}

func seedExportRequest(t *testing.T, s *Store, state ExportRequestState) {
	t.Helper()
	body, err := json.Marshal(state)
	require.NoError(t, err)
	require.NoError(t, s.withWriteTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.ExecContext(context.Background(), "INSERT OR REPLACE INTO meta(key,value) VALUES (?,?)", exportRequestKey, string(body))
		return err
	}, false))
}

func TestExportRequest_InvalidStateFailsClosed(t *testing.T) {
	active := int64(1)
	for _, state := range []ExportRequestState{
		{}, {Version: 2}, {Version: 1, Covered: -1}, {Version: 1, Covered: 2, Requested: 1},
		{Version: 1, Oldest: exportRequestTime().Format(time.RFC3339)},
		{Version: 1, Requested: 1}, {Version: 1, Requested: 1, Oldest: "bad"},
		{Version: 1, Requested: 1, Oldest: exportRequestTime().Format(time.RFC3339), Late: "bad"},
		{Version: 1, Requested: 2, Oldest: exportRequestTime().Format(time.RFC3339), Active: &active},
		{Version: 1, Requested: 1, Oldest: exportRequestTime().Format(time.RFC3339), Active: &active, Late: "bad"},
	} {
		t.Run(fmt.Sprintf("%+v", state), func(t *testing.T) {
			s := exportRequestTestStore(t)
			seedExportRequest(t, s, state)
			require.ErrorIs(t, s.MarkExportRequest(context.Background(), exportRequestTime()), model.ErrInvalidInput)
			_, err := s.ReadExportRequest(context.Background())
			require.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

func TestExportRequest_MetadataFailuresPreserveState(t *testing.T) {
	for _, cause := range []string{"preflight", "cancelled", "native-trigger"} {
		t.Run(cause, func(t *testing.T) {
			s := exportRequestTestStore(t)
			ctx := context.Background()
			require.NoError(t, s.MarkExportRequest(ctx, exportRequestTime()))
			cutoff, err := s.SnapshotExportRequest(ctx)
			require.NoError(t, err)
			before, err := s.ReadExportRequest(ctx)
			require.NoError(t, err)
			switch cause {
			case "preflight":
				s.minFreeBytes = math.MaxUint64
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			case "native-trigger":
				require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
					_, err := tx.ExecContext(ctx, "CREATE TRIGGER owned_export_failure BEFORE INSERT ON meta WHEN NEW.key='meta.export.pending' BEGIN SELECT RAISE(ABORT,'owned metadata failure'); END")
					return err
				}))
			}
			retireErr := s.RetireExportRequest(ctx, cutoff)
			require.Error(t, retireErr)
			if cause == "preflight" {
				require.ErrorIs(t, retireErr, model.ErrDiskFull)
			}
			if cause == "cancelled" {
				require.ErrorIs(t, retireErr, context.Canceled)
			}
			after, err := s.ReadExportRequest(context.Background())
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestExportRequest_StrictDecodeAndReadErrors(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"requested":0,"covered":0,"oldest":"","active":null,"late":"","unknown":1}`,
		`{"version":1,"requested":0,"covered":0,"oldest":"","active":null,"late":""} {}`,
		`{"version":1,"requested":1,"covered":0,"oldest":"2026-10-11T12:00:00+00:00","active":null,"late":""}`,
	} {
		t.Run(raw, func(t *testing.T) {
			s := exportRequestTestStore(t)
			ctx := context.Background()
			require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES (?,?)", exportRequestKey, raw)
				return err
			}))
			_, err := s.ReadExportRequest(ctx)
			require.Error(t, err)
			require.Error(t, s.MarkExportRequest(ctx, exportRequestTime()))
			var actual string
			err = s.QueryRow(ctx, "SELECT value FROM meta WHERE key = ?", exportRequestKey).Scan(&actual)
			require.NoError(t, err)
			require.Equal(t, raw, actual, "corrupt state must never be reset to clean")
		})
	}
	s := exportRequestTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.ReadExportRequest(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestExportRequest_LatchedFailStopPreservesIntent(t *testing.T) {
	s := exportRequestTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.MarkExportRequest(ctx, exportRequestTime()))
	before, err := s.ReadExportRequest(ctx)
	require.NoError(t, err)
	// The normal transaction path classifies a fatal cause; metadata must honor
	// that same latched store state rather than bypassing the write guards.
	fatal := errors.New("database or disk is full (13)")
	require.ErrorIs(t, s.WithTx(ctx, func(tx *sql.Tx) error { return fatal }), fatal)
	require.ErrorIs(t, s.MarkExportRequest(ctx, exportRequestTime()), fatal)
	_, err = s.SnapshotExportRequest(ctx)
	require.ErrorIs(t, err, fatal)
	after, err := s.ReadExportRequest(ctx)
	require.NoError(t, err)
	require.Equal(t, before, after)
}
