// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// Tests of the sticky hub-connected policy (MTIX-112.17.3, ADR-007 §3.5, A9).

func hubMarkFixture(t *testing.T) (*service.HubMarkService, *sqlite.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mark.db")
	st, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return service.NewHubMarkService(st), st, raw
}

func addApplied(t *testing.T, raw *sql.DB, id string) {
	t.Helper()
	_, err := raw.Exec(`INSERT INTO applied_events(event_id, applied_at, applied_by_lamport) VALUES(?, '2026-01-01T00:00:00Z', 1)`, id)
	require.NoError(t, err)
}

func TestHubConnected_NoEvidence_NotConnected(t *testing.T) {
	svc, st, _ := hubMarkFixture(t)
	got, err := svc.HubConnected(context.Background(), service.HubEvidence{})
	require.NoError(t, err)
	require.False(t, got.Connected)
	mark, err := st.HubMark(context.Background())
	require.NoError(t, err)
	require.False(t, mark.Set)
}

func TestHubConnected_EvidenceSources_SetStickyMark(t *testing.T) {
	tests := []struct {
		name   string
		ev     service.HubEvidence
		source string
	}{
		{"config keys", service.HubEvidence{ConfigKeys: true}, "config"},
		{"secrets dsn", service.HubEvidence{SecretsDSN: true}, "secrets"},
		{"env dsn that connected", service.HubEvidence{EnvDSNConnected: true}, "env_dsn"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _, _ := hubMarkFixture(t)
			got, err := svc.HubConnected(context.Background(), tt.ev)
			require.NoError(t, err)
			require.True(t, got.Connected)
			require.True(t, got.Flipped)
			require.Equal(t, tt.source, got.Source)

			// Sticky: the evidence is gone, the mark stays.
			again, err := svc.HubConnected(context.Background(), service.HubEvidence{})
			require.NoError(t, err)
			require.True(t, again.Connected)
			require.False(t, again.Flipped)
			require.Equal(t, tt.source, again.Source)
		})
	}
}

func TestHubConnected_ExportedDSNNoConnection_NotMarked(t *testing.T) {
	t.Setenv("MTIX_SYNC_DSN", "postgres://example.invalid/db")
	svc, _, _ := hubMarkFixture(t)
	got, err := svc.HubConnected(context.Background(), service.HubEvidence{EnvDSNConnected: false})
	require.NoError(t, err)
	require.False(t, got.Connected, "an exported DSN alone is not evidence")
}

func TestHubConnected_HubReachedOnlyThroughEnvDSN_Marks(t *testing.T) {
	svc, _, _ := hubMarkFixture(t)
	flipped, err := svc.RecordEnvConnection(context.Background())
	require.NoError(t, err)
	require.True(t, flipped)
	got, err := svc.HubConnected(context.Background(), service.HubEvidence{})
	require.NoError(t, err)
	require.True(t, got.Connected)
	require.Equal(t, "env_dsn", got.Source)
}

func TestHubConnected_054ApplierRows_Inferred(t *testing.T) {
	svc, st, raw := hubMarkFixture(t)
	addApplied(t, raw, "written-by-054")
	got, err := svc.HubConnected(context.Background(), service.HubEvidence{})
	require.NoError(t, err)
	require.True(t, got.Connected)
	require.True(t, got.Flipped)
	require.Equal(t, "applied_events", got.Source)
	mark, err := st.HubMark(context.Background())
	require.NoError(t, err)
	require.True(t, mark.Set, "inference sets the mark")
}

func TestHubConnected_SyncKeysRemovedAfterEventsApplied_StaysMarked(t *testing.T) {
	svc, _, raw := hubMarkFixture(t)
	_, err := svc.HubConnected(context.Background(), service.HubEvidence{ConfigKeys: true})
	require.NoError(t, err)
	addApplied(t, raw, "e1")
	got, err := svc.HubConnected(context.Background(), service.HubEvidence{})
	require.NoError(t, err)
	require.True(t, got.Connected)
	require.Equal(t, "config", got.Source)
}

func TestInspectHubMark_DoesNotWrite(t *testing.T) {
	svc, st, raw := hubMarkFixture(t)
	addApplied(t, raw, "e1")
	got, err := svc.Inspect(context.Background(), service.HubEvidence{ConfigKeys: true})
	require.NoError(t, err)
	require.True(t, got.Connected)
	require.Equal(t, "applied_events", got.Source)
	mark, err := st.HubMark(context.Background())
	require.NoError(t, err)
	require.False(t, mark.Set, "inspection never writes")

	empty, _, _ := hubMarkFixture(t)
	got, err = empty.Inspect(context.Background(), service.HubEvidence{SecretsDSN: true})
	require.NoError(t, err)
	require.Equal(t, "secrets", got.Source)
}

func TestResetHubMark_FloorPreventsReinference(t *testing.T) {
	svc, _, raw := hubMarkFixture(t)
	addApplied(t, raw, "e1")
	_, err := svc.HubConnected(context.Background(), service.HubEvidence{})
	require.NoError(t, err)

	floor, err := svc.ResetHubMark(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), floor)
	for range 2 {
		got, err := svc.HubConnected(context.Background(), service.HubEvidence{})
		require.NoError(t, err)
		require.False(t, got.Connected, "the next pass does not undo the reset")
	}
}

func TestResetHubMark_NewAppliedRowsAfterReset_MarkAgain(t *testing.T) {
	svc, _, raw := hubMarkFixture(t)
	addApplied(t, raw, "e1")
	_, err := svc.ResetHubMark(context.Background())
	require.NoError(t, err)
	addApplied(t, raw, "e2")
	got, err := svc.HubConnected(context.Background(), service.HubEvidence{})
	require.NoError(t, err)
	require.True(t, got.Connected)
	require.True(t, got.Flipped)
}

type failingMarkStore struct{ err error }

func (f failingMarkStore) HubMark(context.Context) (sqlite.HubMark, error) {
	return sqlite.HubMark{}, f.err
}
func (f failingMarkStore) MarkHubConnected(context.Context, string) (bool, error) {
	return false, f.err
}
func (f failingMarkStore) InferHubMark(context.Context) (bool, error) { return false, f.err }
func (f failingMarkStore) AppliedEventsAboveFloor(context.Context) (bool, error) {
	return false, f.err
}
func (f failingMarkStore) ResetHubMark(context.Context) (int64, error) { return 0, f.err }

func TestHubMarkService_StoreErrors_ArePropagatedWrapped(t *testing.T) {
	boom := errors.New("store down")
	svc := service.NewHubMarkService(failingMarkStore{err: boom})
	ctx := context.Background()
	_, err := svc.HubConnected(ctx, service.HubEvidence{})
	require.ErrorIs(t, err, boom)
	_, err = svc.Inspect(ctx, service.HubEvidence{})
	require.ErrorIs(t, err, boom)
	_, err = svc.RecordEnvConnection(ctx)
	require.ErrorIs(t, err, boom)
	_, err = svc.ResetHubMark(ctx)
	require.ErrorIs(t, err, boom)
}

func TestHubConnected_MarkWriteFailsAfterInference_Propagates(t *testing.T) {
	boom := errors.New("write failed")
	svc := service.NewHubMarkService(partialFailStore{failingMarkStore{err: boom}})
	_, err := svc.HubConnected(context.Background(), service.HubEvidence{ConfigKeys: true})
	require.ErrorIs(t, err, boom)
}

// partialFailStore reads cleanly (no mark, no rows) but fails every write.
type partialFailStore struct{ failingMarkStore }

func (partialFailStore) HubMark(context.Context) (sqlite.HubMark, error) {
	return sqlite.HubMark{}, nil
}
func (partialFailStore) InferHubMark(context.Context) (bool, error) { return false, nil }
func (partialFailStore) AppliedEventsAboveFloor(context.Context) (bool, error) {
	return false, nil
}

// readFailStore reads the mark cleanly but fails the applied-events probe.
type readFailStore struct{ failingMarkStore }

func (readFailStore) HubMark(context.Context) (sqlite.HubMark, error) { return sqlite.HubMark{}, nil }

func TestInspectHubMark_AppliedEventsProbeFails_Propagates(t *testing.T) {
	boom := errors.New("probe failed")
	svc := service.NewHubMarkService(readFailStore{failingMarkStore{err: boom}})
	_, err := svc.Inspect(context.Background(), service.HubEvidence{})
	require.ErrorIs(t, err, boom)
}

func TestHubConnected_InferenceFails_Propagates(t *testing.T) {
	boom := errors.New("infer failed")
	svc := service.NewHubMarkService(readFailStore{failingMarkStore{err: boom}})
	_, err := svc.HubConnected(context.Background(), service.HubEvidence{})
	require.ErrorIs(t, err, boom)
}
