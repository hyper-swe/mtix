// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// A re-arm after a wake still sees the delivered mail as unacked. The wait must
// skip it and end only for new mail (MTIX-112.40.4, round 2 F1).
func TestClaudeHook_ReArmWithDeliveredUnackedMail_WaitsForNewMail(t *testing.T) {
	r := newHookRig(t)
	r.claims.claimed[5] = true // delivered by the previous run; still unacked in the inbox
	calls := 0
	var skipped5, skipped9 bool
	r.deps.Wait = func(_ context.Context, skip func(int64) bool) ([]sqlite.InboxEvent, error) {
		calls++
		all := []sqlite.InboxEvent{hookEvent(5, "old")}
		if calls > 1 {
			all = append(all, hookEvent(9, "new")) // new mail arrives while waiting
		}
		var out []sqlite.InboxEvent
		for _, e := range all {
			if !skip(e.Seq) {
				out = append(out, e)
			}
		}
		skipped5, skipped9 = skip(5), skip(9)
		return out, nil
	}

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookExitWake, res.ExitCode)
	assert.Equal(t, "ENVELOPE new", res.Envelope, "only the new mail wakes the session")
	assert.Equal(t, 2, calls, "the lost claim on old mail did not end the wait")
	assert.True(t, skipped5, "the lost event is skipped by the next wait")
	assert.False(t, skipped9, "an unseen event is not skipped")
	assert.Equal(t, model.WakeDelivered, r.claims.outcomes[9])
	assert.False(t, r.leases.held)
}

func TestClaudeHook_ReArmWithOnlyDeliveredMail_ExitsEmptyAtTimeout(t *testing.T) {
	r := newHookRig(t)
	r.claims.claimed[5] = true
	calls := 0
	r.deps.Wait = func(_ context.Context, skip func(int64) bool) ([]sqlite.InboxEvent, error) {
		calls++
		if calls == 1 {
			return []sqlite.InboxEvent{hookEvent(5, "old")}, nil
		}
		assert.True(t, skip(5))
		return nil, nil // the wait timed out with nothing new
	}

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookEmpty, res.Outcome)
	assert.Zero(t, res.ExitCode)
	assert.Equal(t, 2, calls)
}

// A claim error mid-loop must not lose the claims already won (round 2 F2).
func TestClaudeHook_ClaimFailsMidLoop_DeliversWonClaimsAndRecordsFailure(t *testing.T) {
	r := newHookRig(t, hookEvent(3, "first"), hookEvent(4, "second"))
	r.claims.failSeq = 4

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookExitWake, res.ExitCode, "the won claim still wakes the session")
	assert.Equal(t, "ENVELOPE first", res.Envelope)
	assert.Equal(t, model.WakeDelivered, r.claims.outcomes[3], "the earlier claim is settled, not left claimed")
	_, settled := r.claims.outcomes[4]
	assert.False(t, settled)
	assert.False(t, r.claims.claimed[4], "the failed event stays unclaimed for the next re-arm")
	failures, err := r.log.Failures()
	require.NoError(t, err)
	require.Len(t, failures, 1, "the claim failure is recorded for doctor")
	assert.Contains(t, failures[0].Error, "claim wake for event 4")
	assert.False(t, r.leases.held)
}

func TestClaudeHook_ClaimFailsWithNothingWon_IsAnErrorExitZero(t *testing.T) {
	r := newHookRig(t, hookEvent(4, "only"))
	r.claims.failSeq = 4

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookFailed, res.Outcome)
	assert.Zero(t, res.ExitCode)
}

// A failed outcome write after a won claim still delivers the envelope (round 2 F3).
func TestClaudeHook_OutcomeWriteFails_StillDeliversEnvelopeAndRecords(t *testing.T) {
	r := newHookRig(t, hookEvent(8, "urgent"))
	r.claims.recErr = assert.AnError

	res := NewClaudeHook(r.deps).Run(t.Context(), ClaudeHookStop)

	assert.Equal(t, ClaudeHookExitWake, res.ExitCode, "the claimed event must wake the session")
	assert.Equal(t, "ENVELOPE urgent", res.Envelope)
	assert.Equal(t, ClaudeHookWoke, res.Outcome)
	failures, err := r.log.Failures()
	require.NoError(t, err)
	require.Len(t, failures, 1)
	assert.Contains(t, failures[0].Error, "record wake outcome for event 8")
	assert.False(t, r.leases.held, "the lease is released")
}
