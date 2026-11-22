// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-130.20.3: the wake scanner's per-pass writes (registration marks and
// touches, scan cursor, leases, holds, wake outcomes) and the dispatcher
// heartbeat touch only tables the board export never reads, so they must not
// run the on-commit callbacks that record an export request.
func TestWakeAndRegistrationBookkeeping_DoNotRunOnCommitCallbacks(t *testing.T) {
	s, now := reclaimStore(t)
	seedExportedRows(t, s)
	ctx := t.Context()
	rkey := sqlite.RegistrationKey{AgentID: "w", HostID: "h", UID: "u"}
	stamp := now.UTC().Format(time.RFC3339Nano)
	_, err := s.RegisterSessionAtomic(ctx, rkey, func(*sqlite.Registration, int64) (*sqlite.Registration, error) {
		return &sqlite.Registration{AgentID: "w", HostID: "h", UID: "u", Harness: "x", SessionHandle: "sess", Origin: "test",
			Epoch: 1, RegisteredAt: stamp, LastSeen: stamp, RegistrarVersion: "v", AttachProtocol: 1}, nil
	})
	require.NoError(t, err)
	wkey := model.WakeKey{AgentID: "w", HostID: "h", UID: "u", EventSeq: 1, Epoch: 1}
	attempt, won, err := s.ClaimWake(ctx, wkey, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	wkey2 := model.WakeKey{AgentID: "w", HostID: "h", UID: "u", EventSeq: 2, Epoch: 1}
	attempt2, won, err := s.ClaimWake(ctx, wkey2, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	var lease sqlite.WakeLease
	lkey := sqlite.WakeLeaseKey{AgentID: "w", HostID: "h", UID: "u"}
	var calls int
	s.SetOnCommit(func() { calls++ })
	tests := []struct {
		name  string
		do    func() error
		table string // a table the write must have changed, proving it ran
	}{
		{"record wake outcome", func() error { return s.RecordWakeOutcome(ctx, attempt2, model.WakeDelivered) }, "wake_ledger"},
		{"record wake result", func() error {
			return s.RecordWakeResult(ctx, attempt, model.WakeResult{Outcome: model.WakeSent, Route: "r", At: stamp})
		}, "wake_ledger"},
		{"upgrade wake confirmed", func() error { return s.UpgradeWakeConfirmed(ctx, wkey) }, "wake_ledger"},
		{"mark registration stale", func() error { _, err := s.MarkRegistrationStale(ctx, rkey, 1, stamp); return err }, "agent_registrations"},
		{"mark registration stale again (zero rows)", func() error { _, err := s.MarkRegistrationStale(ctx, rkey, 1, stamp); return err }, ""},
		{"advance wake scan cursor", func() error { return s.AdvanceWakeScanCursor(ctx, "h", "u", 3, time.Minute) }, "wake_scan_cursor"},
		{"set wake hold", func() error { return s.SetWakeHold(ctx, wkey, 1, true) }, "agent_registrations"},
		{"touch registration", func() error {
			_, err := s.TouchRegistration(ctx, rkey, "sess", now.Add(time.Minute).UTC().Format(time.RFC3339Nano))
			return err
		}, "agent_registrations"},
		{"acquire wake lease", func() error {
			var err error
			lease, err = s.AcquireWakeLease(ctx, lkey, "0123456789abcdef0123456789abcdef", time.Minute)
			return err
		}, "wake_leases"},
		{"release wake lease", func() error { _, err := s.ReleaseWakeLease(ctx, lkey, lease.Holder, time.Minute, false); return err }, "wake_leases"},
		{"dispatcher heartbeat", func() error {
			return s.RecordDispatcherHeartbeat(ctx, sqlite.DispatcherHeartbeat{HostID: "h", UID: "u", PID: 1, SeenAt: stamp})
		}, "dispatcher_heartbeat"},
		{"delete registration if", func() error {
			_, err := s.DeleteRegistrationIf(ctx, rkey, sqlite.RegistrationSnapshot{Epoch: 1, LastSeen: stamp})
			return err
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, rows, tableBefore := calls, exportedRows(t, s), tableDump(t, s, tt.table)
			require.NoError(t, tt.do())
			require.Equal(t, before, calls, "bookkeeping commit ran the on-commit callbacks")
			require.Equal(t, rows, exportedRows(t, s), "bookkeeping commit changed exported content")
			if tt.table != "" {
				require.NotEqual(t, tableBefore, tableDump(t, s, tt.table), "the write did not land in %s", tt.table)
			}
		})
	}
}

func tableDump(t *testing.T, s *sqlite.Store, table string) string {
	t.Helper()
	if table == "" {
		return ""
	}
	return dumpTable(t, s, table)
}
