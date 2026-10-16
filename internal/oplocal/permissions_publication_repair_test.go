// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Protected-file faults after an idempotent publisher returns must preserve the
// accepted batch and allow repair without a new confirmation or occurrence.
package oplocal

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type permissionPublicationDamage struct {
	t            *testing.T
	state        *State
	kind, name   string
	original     []byte
	fired        bool
	publications map[string]bool
}

func (f *permissionPublicationDamage) publish(_ context.Context, b PermissionBatch) error {
	f.publications[b.ID] = true
	if f.fired {
		return nil
	}
	f.fired = true
	f.name = "permissions-replays.json"
	if f.kind == "log" {
		f.name = "confirmations.jsonl"
	}
	var present bool
	f.original, present = contractReadFile(f.t, f.state, f.name)
	require.True(f.t, present)
	if f.kind != "canonical_receipt" {
		writePermissionFixture(f.t, f.state, f.name, []byte("{synthetic protected-file fault\n"))
		return nil
	}
	rows := generationReplayRows(f.t, f.state)
	require.Len(f.t, rows, 1)
	rows[0].Batch.Time = rows[0].Batch.Time.Add(time.Hour)
	rows[0].BatchHash = permissionBatchHash(rows[0].Batch)
	require.NoError(f.t, f.state.WriteJSON("permissions-replays", rows))
	return nil
}

func TestPermissions_PostpublicationProtectedFaultRecoversOriginalAcceptedBatch(t *testing.T) {
	for _, kind := range []string{"log", "receipt", "canonical_receipt"} {
		t.Run(kind, func(t *testing.T) { testPermissionPublicationRepair(t, kind) })
	}
}

func testPermissionPublicationRepair(t *testing.T, kind string) {
	t.Helper()
	s := alertFixture(t)
	fault := &permissionPublicationDamage{t: t, state: s, kind: kind, publications: map[string]bool{}}
	prepare := func(ctx context.Context, b PermissionBatch) (PermissionBatch, error) {
		b, err := replayPrepare(ctx, b)
		b.NodeID = "TEST-1"
		return b, err
	}
	intent := replayIntent()
	require.Error(t, s.ApplyPermissionIntent(t.Context(), intent, prepare, fault.publish))
	require.True(t, fault.fired)
	pending := generationPending(t, s)
	require.NotNil(t, pending.Batch)
	writePermissionFixture(t, s, fault.name, fault.original)
	require.NoError(t, New(s.env).ApplyPermissionIntent(t.Context(), intent, nil, fault.publish))
	require.Len(t, fault.publications, 1, "fixture publisher is idempotent; not a real SQLite event-count claim")
	result, available, err := s.PermissionReplayResult(t.Context(), intent.BoardID, intent.ID)
	require.NoError(t, err)
	require.True(t, available)
	require.Equal(t, *pending.Batch, result)
	require.Equal(t, pending.Generation, generationPending(t, s).Generation)
	rows, err := s.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, pending.Batch.ID, rows[0].ID)
	require.False(t, rows[0].Pending)
	require.Equal(t, "published", rows[0].BoardLine)
}
