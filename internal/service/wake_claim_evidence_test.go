// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that the production routes settle a reclaimed wake from the recipient's inbox
// acknowledgement without anyone wiring an evidence source (MTIX-112.40.13).
package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// addressedSeq makes one real inbox event for the recipient and returns its sequence.
func addressedSeq(t *testing.T, st *sqlite.Store) int64 {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, st.CreateNode(t.Context(), &model.Node{ID: "TEST-1", Project: "TEST", Depth: 0, Seq: 1, Title: "t", Status: model.StatusOpen, Priority: model.PriorityMedium, NodeType: model.NodeTypeStory, Weight: 1.0, Creator: "test-agent", ContentHash: model.ComputeContentHash("t", "", "", "", nil), CreatedAt: now, UpdatedAt: now}))
	require.NoError(t, st.SetAnnotations(t.Context(), "TEST-1", []model.Annotation{{ID: "ann1", Author: "sender", Text: "ruling", CreatedAt: now, Addressee: bothRoutesRecipient.AgentID}}))
	events, err := st.InboxList(t.Context(), bothRoutesRecipient.AgentID)
	require.NoError(t, err)
	require.Len(t, events, 1)
	return events[0].Seq
}

func TestWakeEvidence_Production_DispatcherReclaimOfAcknowledgedEventDoesNotPushAgain(t *testing.T) {
	for _, acked := range []bool{true, false} {
		t.Run(map[bool]string{true: "acknowledged", false: "unread"}[acked], func(t *testing.T) {
			st, now, alerts := wakeServiceStore(t)
			seq := addressedSeq(t, st)
			_, won, err := st.ClaimWake(t.Context(), bothRoutesKey(seq), time.Minute)
			require.NoError(t, err)
			require.True(t, won, "an earlier dispatcher claimed, pushed and crashed")
			if acked {
				require.NoError(t, st.InboxAck(t.Context(), bothRoutesRecipient.AgentID, seq))
			}
			*now = now.Add(2 * time.Minute)
			tally := &wakeTally{wakes: map[int64]int{}}
			d := bothRoutesDispatcher(t, st, alerts, tally, nil) // no evidence passed: production default
			require.NoError(t, d.Pass(t.Context(), []model.WakeKey{bothRoutesKey(seq)}))
			if acked {
				require.Zero(t, tally.wakes[seq], "the session already read it: no second push")
				got, found, err := st.ReadWakeResult(t.Context(), bothRoutesKey(seq))
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, model.WakeConfirmed, got.Outcome)
			} else {
				require.Equal(t, 1, tally.wakes[seq], "unread: exactly one redelivery, never zero")
			}
		})
	}
}

func TestWakeEvidence_Production_HookReclaimOfAcknowledgedEventDoesNotWakeAgain(t *testing.T) {
	st, now, _ := wakeServiceStore(t)
	seq := addressedSeq(t, st)
	_, won, err := st.ClaimWake(t.Context(), bothRoutesKey(seq), time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, st.InboxAck(t.Context(), bothRoutesRecipient.AgentID, seq))
	*now = now.Add(2 * time.Minute)
	res := hookForEvent(t, st, seq, nil).Run(t.Context(), service.ClaudeHookStop)
	require.Zero(t, res.ExitCode, "the event was already read: the hook must not wake again")
}

type stubAckReader struct {
	acked bool
	err   error
}

func (s stubAckReader) InboxEventAcked(context.Context, string, int64) (bool, error) {
	return s.acked, s.err
}

func TestInboxAckEvidence_MapsKeyToAgentAndSeq(t *testing.T) {
	var gotAgent string
	var gotSeq int64
	reader := readerFunc(func(_ context.Context, agent string, seq int64) (bool, error) {
		gotAgent, gotSeq = agent, seq
		return true, nil
	})
	seen, err := service.InboxAckEvidence(reader)(t.Context(), bothRoutesKey(7))
	require.NoError(t, err)
	require.True(t, seen)
	require.Equal(t, bothRoutesRecipient.AgentID, gotAgent)
	require.EqualValues(t, 7, gotSeq)
	_, err = service.InboxAckEvidence(stubAckReader{err: errors.New("read failed")})(t.Context(), bothRoutesKey(7))
	require.Error(t, err)
}

type readerFunc func(context.Context, string, int64) (bool, error)

func (f readerFunc) InboxEventAcked(ctx context.Context, agent string, seq int64) (bool, error) {
	return f(ctx, agent, seq)
}

// The production store is the evidence source both routes default to.
var _ service.InboxAckReader = (*sqlite.Store)(nil)
