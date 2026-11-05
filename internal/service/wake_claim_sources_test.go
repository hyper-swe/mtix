// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type wakeClaimSourceFixture struct {
	store   *sqlite.Store
	prompts *PromptService
	board   *sqlite.ExportData
	events  []sqlite.InboxEvent
	key     model.WakeKey
}

func newWakeClaimSourceFixture(t *testing.T) wakeClaimSourceFixture {
	t.Helper()
	s, err := sqlite.New(filepath.Join(t.TempDir(), "wake.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	clock := func() time.Time { return time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC) }
	s.SetClock(clock)
	nodes := NewNodeService(s, &NoopBroadcaster{}, &StaticConfig{}, slog.Default(), clock)
	prompts := NewPromptService(s, &NoopBroadcaster{}, slog.Default(), clock)
	_, err = nodes.CreateNode(t.Context(), &CreateNodeRequest{Project: "PROJ", Title: "survivor", Creator: "sender"})
	require.NoError(t, err)
	board, err := s.Export(t.Context(), "", "")
	require.NoError(t, err)
	key := model.WakeKey{AgentID: "worker", HostID: "host", UID: "1000", Epoch: 1}
	_, err = s.RegisterSessionAtomic(t.Context(), sqlite.RegistrationKey{AgentID: key.AgentID, HostID: key.HostID, UID: key.UID},
		func(_ *sqlite.Registration, tail int64) (*sqlite.Registration, error) {
			return &sqlite.Registration{AgentID: key.AgentID, HostID: key.HostID, UID: key.UID, Epoch: key.Epoch,
				Harness: "pi", SessionHandle: "test-session", Origin: "manual", HighWater: tail,
				RegisteredAt: "2026-11-06T00:00:00Z", LastSeen: "2026-11-06T00:00:00Z", RegistrarVersion: "test", AttachProtocol: 1}, nil
		})
	require.NoError(t, err)
	dropped, err := nodes.CreateNode(t.Context(), &CreateNodeRequest{Project: "PROJ", Title: "removed", Creator: "sender"})
	require.NoError(t, err)
	require.NoError(t, prompts.AddAnnotation(t.Context(), dropped.ID, "old", "sender", key.AgentID))
	events, err := s.InboxList(t.Context(), key.AgentID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	return wakeClaimSourceFixture{store: s, prompts: prompts, board: board, events: events, key: key}
}

func (f wakeClaimSourceFixture) replace(t *testing.T) {
	t.Helper()
	_, err := f.store.Import(t.Context(), f.board, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	for _, body := range []string{"fresh first", "fresh second"} {
		require.NoError(t, f.prompts.AddAnnotation(t.Context(), "PROJ-1", body, "sender", f.key.AgentID))
	}
	fresh, err := f.store.InboxList(t.Context(), f.key.AgentID)
	require.NoError(t, err)
	require.Len(t, fresh, 2)
	require.Equal(t, f.events[0].Seq, fresh[1].Seq)
	require.NotEqual(t, f.events[0].EventID, fresh[1].EventID)
}

type replaceOnWakeClaim struct {
	*sqlite.Store
	before func()
}

func (s replaceOnWakeClaim) ClaimWake(ctx context.Context, key model.WakeKey, lease time.Duration) (model.WakeAttempt, bool, error) {
	s.before()
	return s.Store.ClaimWake(ctx, key, lease)
}

func TestWakeClaimSources_ReplaceBeforeClaim_OldSnapshotRefused(t *testing.T) {
	for _, source := range []string{"pi wait", "session hook"} {
		t.Run(source, func(t *testing.T) {
			f := newWakeClaimSourceFixture(t)
			wrapped := replaceOnWakeClaim{Store: f.store, before: func() { f.replace(t) }}
			if source == "pi wait" {
				p := PiWait{opts: PiWaitOptions{Lease: time.Minute}}
				attempt, won, err := p.tryClaim(t.Context(), wrapped, f.key.HostID, f.key.UID, "test-session")
				require.NoError(t, err)
				assert.False(t, won)
				assert.Empty(t, attempt.Token)
			} else {
				h := NewClaudeHook(ClaudeHookDeps{Key: WakeLeaseKey{AgentID: f.key.AgentID, HostID: f.key.HostID, UID: f.key.UID}, Claims: wrapped, ClaimLease: time.Minute})
				events, attempts, err := h.claimEvents(t.Context(), f.events, f.key.Epoch, map[int64]bool{})
				require.NoError(t, err)
				assert.Empty(t, events)
				assert.Empty(t, attempts)
			}
			stats, err := f.store.WakeLedgerStats(t.Context())
			require.NoError(t, err)
			assert.Zero(t, stats.Rows, "no fresh event inherited an old candidate's claim")
			assertFreshWakeClaims(t, f)
		})
	}
}

func assertFreshWakeClaims(t *testing.T, f wakeClaimSourceFixture) {
	t.Helper()
	fresh, err := f.store.InboxList(t.Context(), f.key.AgentID)
	require.NoError(t, err)
	for _, event := range fresh {
		key := f.key
		key.EventSeq, key.EventID = event.Seq, event.EventID
		attempt, won, err := f.store.ClaimWake(t.Context(), key, time.Minute)
		require.NoError(t, err)
		require.True(t, won)
		require.NoError(t, f.store.RecordWakeOutcome(t.Context(), attempt, model.WakeDelivered))
		_, won, err = f.store.ClaimWake(t.Context(), key, time.Minute)
		require.NoError(t, err)
		assert.False(t, won)
	}
}
