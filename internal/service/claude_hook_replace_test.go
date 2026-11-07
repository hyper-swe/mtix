// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func replaceHookMailAtSameSequence(t *testing.T, f wakeClaimSourceFixture) {
	t.Helper()
	_, err := f.store.Import(t.Context(), f.board, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	nodes := NewNodeService(f.store, &NoopBroadcaster{}, &StaticConfig{}, slog.Default(), time.Now)
	_, err = nodes.CreateNode(t.Context(), &CreateNodeRequest{Project: "PROJ", Title: "unaddressed padding", Creator: "sender"})
	require.NoError(t, err)
	require.NoError(t, f.prompts.AddAnnotation(t.Context(), "PROJ-1", "fresh only", "sender", f.key.AgentID))
	fresh, err := f.store.InboxList(t.Context(), f.key.AgentID)
	require.NoError(t, err)
	require.Len(t, fresh, 1)
	require.Equal(t, f.events[0].Seq, fresh[0].Seq)
	require.NotEqual(t, f.events[0].EventID, fresh[0].EventID)
}

func claimOldHookMail(t *testing.T, f wakeClaimSourceFixture) {
	t.Helper()
	key := f.key
	key.EventSeq, key.EventID = f.events[0].Seq, f.events[0].EventID
	attempt, won, err := f.store.ClaimWake(t.Context(), key, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, f.store.RecordWakeOutcome(t.Context(), attempt, model.WakeDelivered))
}

func replacingHook(t *testing.T, f wakeClaimSourceFixture, order string) *ClaudeHook {
	t.Helper()
	replaced, waits := false, 0
	replace := func() { replaceHookMailAtSameSequence(t, f); replaced = true }
	claims := replaceOnWakeClaim{Store: f.store, before: func() {
		if order == "before claim" && !replaced {
			replace()
		}
	}}
	return NewClaudeHook(ClaudeHookDeps{
		Key:    WakeLeaseKey{AgentID: f.key.AgentID, HostID: f.key.HostID, UID: f.key.UID},
		Claims: claims, ClaimLease: time.Minute,
		Epoch: func(context.Context) (int64, error) { return f.key.Epoch, nil },
		Wait: func(ctx context.Context, skip func(sqlite.InboxEvent) bool) ([]sqlite.InboxEvent, error) {
			waits++
			require.LessOrEqual(t, waits, 5, "the waiter must not spin on lost claims")
			if waits == 2 {
				require.True(t, skip(f.events[0]), "the exact old identity remains skipped")
			}
			if order == "during wait" && waits == 2 {
				replace()
			}
			events, err := f.store.InboxList(ctx, f.key.AgentID)
			var fresh []sqlite.InboxEvent
			for _, e := range events {
				if !skip(e) {
					fresh = append(fresh, e)
				}
			}
			return fresh, err
		},
		Render: func(events []sqlite.InboxEvent) (string, error) {
			require.Len(t, events, 1)
			return events[0].EventID, nil
		},
	})
}

func TestClaudeHook_ReplaceReusesLostClaimSequence_FreshIdentityWakes(t *testing.T) {
	for _, order := range []string{"during wait", "before claim"} {
		t.Run(order, func(t *testing.T) {
			f := newWakeClaimSourceFixture(t)
			if order == "during wait" {
				claimOldHookMail(t, f)
			}
			h := replacingHook(t, f, order)
			res, err := h.wakeOnce(t.Context())
			require.NoError(t, err)
			fresh, err := f.store.InboxList(t.Context(), f.key.AgentID)
			require.NoError(t, err)
			require.Len(t, fresh, 1)
			assert.Equal(t, ClaudeHookWoke, res.Outcome)
			assert.Equal(t, ClaudeHookExitWake, res.ExitCode)
			assert.Equal(t, fresh[0].EventID, res.Envelope, "the only fresh event is at the reused sequence")
			res, err = h.wakeOnce(t.Context())
			require.NoError(t, err)
			assert.Equal(t, ClaudeHookEmpty, res.Outcome, "the fresh identity wakes only once")
		})
	}
}
