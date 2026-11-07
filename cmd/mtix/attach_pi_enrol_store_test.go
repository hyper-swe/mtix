// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func seedPiEnrolForwarding(t *testing.T, root string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.New(filepath.Join(root, ".mtix", "data", "mtix.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	_, err = s.RegisterSessionAtomic(t.Context(), sqlite.RegistrationKey{AgentID: "worker", HostID: "test-host", UID: "501"}, func(_ *sqlite.Registration, _ int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{AgentID: "worker", HostID: "test-host", UID: "501", Harness: "pi", Origin: "manual", RegisteredAt: "2026-11-07T00:00:00Z", LastSeen: "2026-11-07T00:00:00Z", RegistrarVersion: "test", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	// A real addressed journal row makes reads and the claim/result observable.
	_, err = s.WriteDB().ExecContext(t.Context(), `INSERT INTO sync_events
		(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,sync_status,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, "pi-enrol-forward", "TEST", "TEST-1", "comment", `{"to":"worker","body":"hello"}`, 1, 1, "{}", "sender", "0123456789abcdef", "pending", "2026-11-07T00:00:00Z")
	require.NoError(t, err)
	return s
}

func TestEnrolGuard_PiStorePolling_ReleasedLockForwardsRealClaim(t *testing.T) {
	state := testutil.OperatorState(t)
	root := listedBoardCheckout(t, state)
	seed := seedPiEnrolForwarding(t, root)
	prepareEnrolGuardCase(t, root, "released")
	s, closeStore, err := openPiWaitBoard(attach.WithStoreRoot(t.Context(), root))
	require.NoError(t, err)
	defer func() { require.NoError(t, closeStore()) }()
	regs, err := s.ListRegistrations(t.Context(), "test-host", "501")
	require.NoError(t, err)
	require.Len(t, regs, 1)
	assert.Equal(t, "worker", regs[0].AgentID)
	events, err := s.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "hello", events[0].Body)
	key := model.WakeKey{AgentID: "worker", HostID: "test-host", UID: "501", EventSeq: events[0].Seq, EventID: events[0].EventID, Epoch: regs[0].Epoch}
	attempt, won, err := s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.RecordWakeResult(t.Context(), attempt, model.WakeResult{Outcome: model.WakeSent, Route: "pi"}))
	var outcome string
	require.NoError(t, seed.ReadDB().QueryRowContext(t.Context(), "SELECT outcome FROM wake_ledger WHERE agent_id=?", "worker").Scan(&outcome))
	assert.Equal(t, string(model.WakeSent), outcome)
	_, won, err = s.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	assert.False(t, won, "the real terminal result prevents a duplicate claim")
}
