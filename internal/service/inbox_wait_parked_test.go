// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.3: every production wait path goes through InboxReadWait, so the
// waiter row must be recorded there, with or without a board policy.
func TestInboxReadWait_RecordsWaiterForTheWholeWait_WithAndWithoutPolicy(t *testing.T) {
	allow := func(context.Context, string) (InboxEligibility, error) { return InboxEligibility{}, nil }
	tests := []struct {
		name   string
		policy InboxPolicy
	}{
		{"with a board policy", InboxPolicy{Eligibility: allow}},
		{"without a policy", InboxPolicy{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := sqlite.New(t.TempDir(), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { _ = st.Close() })
			svc := NewInboxService(st).WithInboxPolicy(tt.policy)
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = svc.InboxReadWait(t.Context(), "w", 1500*time.Millisecond, InboxReadOptions{})
			}()
			require.Eventually(t, func() bool {
				p, perr := st.InboxParked(t.Context(), "w")
				return perr == nil && p.Parked
			}, 3*time.Second, 20*time.Millisecond, "parked during the wait")
			<-done
			p, err := st.InboxParked(t.Context(), "w")
			require.NoError(t, err)
			require.False(t, p.Parked, "not parked after the wait ends")
			var rows int
			require.NoError(t, st.WriteDB().QueryRow(`SELECT COUNT(*) FROM agent_inbox_waiters`).Scan(&rows))
			require.Zero(t, rows, "the waiter row is removed on exit")
		})
	}
}

// The wait ends on caller cancellation: the row is still removed.
func TestInboxReadWait_PolicyWaitCancelled_RemovesWaiter(t *testing.T) {
	st, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	svc := NewInboxService(st).WithInboxPolicy(InboxPolicy{
		Eligibility: func(context.Context, string) (InboxEligibility, error) { return InboxEligibility{}, nil }})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, werr := svc.InboxReadWait(ctx, "w", 0, InboxReadOptions{})
		done <- werr
	}()
	require.Eventually(t, func() bool {
		p, perr := st.InboxParked(t.Context(), "w")
		return perr == nil && p.Parked
	}, 3*time.Second, 20*time.Millisecond)
	cancel()
	require.Error(t, <-done)
	p, err := st.InboxParked(t.Context(), "w")
	require.NoError(t, err)
	require.False(t, p.Parked)
}
