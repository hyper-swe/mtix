// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests that an event acknowledged between the candidate read and the reopen is settled as
// confirmed with no budget charge (MTIX-112.40.12, FR-20).
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestReopenStalledWake_AckedBetweenReadAndReopen_SettlesConfirmedWithoutCharge(t *testing.T) {
	now := wakeTime()
	s := openWakeStore(t, t.TempDir()+"/race.db", now)
	key := stallKey(1)
	sentRow(t, s, key)
	s.SetClock(func() time.Time { return now.Add(time.Minute) })
	// The session reads the event after the candidate read and before the reopen.
	_, err := s.WriteDB().ExecContext(context.Background(),
		`INSERT INTO agent_inbox_ack(agent_id,event_seq,acked_at) VALUES(?,?,?)`, key.AgentID, key.EventSeq, now.Format(time.RFC3339Nano))
	require.NoError(t, err)
	var proposals []model.WakeBudgetProposal
	res, err := s.ReopenStalledWake(context.Background(), key, 30*time.Second, func(p model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
		proposals = append(proposals, p)
		return model.WakeBudgetDecision{Allow: p.Claimable}, nil
	})
	require.NoError(t, err)
	require.False(t, res.Won, "an acknowledged event is not reopened")
	for _, p := range proposals {
		require.False(t, p.Claimable, "the proposal never offers the acknowledged event for a budget charge")
	}
	got, _, err := s.ReadWakeResult(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, model.WakeConfirmed, got.Outcome, "the acknowledged event settles as confirmed")
}
