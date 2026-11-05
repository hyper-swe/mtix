// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func wakeIdentityRows(t *testing.T, f wakeReplaceState) [][]string {
	t.Helper()
	return [][]string{
		wakeReplaceRows(t, f.raw, `SELECT * FROM wake_ledger ORDER BY agent_id,host_id,uid,event_seq,epoch`, nil),
		wakeReplaceRows(t, f.raw, `SELECT * FROM agent_registrations ORDER BY agent_id,host_id,uid`, nil),
		wakeReplaceRows(t, f.raw, `SELECT * FROM wake_budget_counters ORDER BY agent_id,host_id,uid`, nil),
		wakeReplaceRows(t, f.raw, `SELECT * FROM meta WHERE key LIKE 'wake%' ORDER BY key`, nil),
	}
}

func refusedWakeOperation(t *testing.T, s *sqlite.Store, key model.WakeKey, operation string) error {
	t.Helper()
	switch operation {
	case "claim":
		_, won, err := s.ClaimWake(t.Context(), key, time.Minute)
		assert.False(t, won)
		return err
	case "accounted claim":
		called := false
		result, err := s.ClaimWakeBudget(t.Context(), key, time.Minute, func(p model.WakeBudgetProposal) (model.WakeBudgetDecision, error) {
			called = true
			return model.WakeBudgetDecision{BoardID: p.BoardCandidate, Allow: true}, nil
		})
		assert.False(t, called, "replaced or unreadable candidates never reach protected admission")
		assert.False(t, result.Won)
		assert.Equal(t, err == nil, result.Replaced, "only a successful identity read can establish a quiet replacement")
		return err
	case "hold":
		return s.SetWakeHold(t.Context(), key, 0, true)
	case "advance":
		return s.SetWakeHold(t.Context(), key, key.EventSeq, false)
	}
	t.Fatalf("unknown wake operation %q", operation)
	return nil
}

func replaceWakeIdentityKey(t *testing.T, f wakeReplaceState) model.WakeKey {
	t.Helper()
	key := model.WakeKey{AgentID: "worker", HostID: "removed-host", UID: "1000", Epoch: 7, EventSeq: f.dropped}
	require.NoError(t, f.raw.QueryRow(`SELECT event_id FROM sync_events WHERE rowid=?`, key.EventSeq).Scan(&key.EventID))
	require.NotEmpty(t, key.EventID)
	return key
}

func TestWakeIdentity_RemovedOrReusedEvent_RefusesMutationWithoutAdmission(t *testing.T) {
	for _, regrow := range []bool{false, true} {
		for _, operation := range []string{"claim", "accounted claim", "hold", "advance"} {
			t.Run(operation+map[bool]string{false: "/removed", true: "/reused"}[regrow], func(t *testing.T) {
				f := newWakeReplaceState(t, "above", false)
				key := replaceWakeIdentityKey(t, f)
				_, err := f.store.Import(t.Context(), f.data, sqlite.ImportModeReplace, false)
				require.NoError(t, err)
				if regrow {
					addressTo(t, f.store, "worker", "PROJ-3")
					tail, tailErr := f.store.JournalTail(t.Context())
					require.NoError(t, tailErr)
					require.Equal(t, key.EventSeq, tail)
				}
				// A stale hold must be observable as a change, independently of mark advancement.
				_, err = f.raw.Exec(`UPDATE agent_registrations SET pending_hold=0 WHERE host_id=?`, key.HostID)
				require.NoError(t, err)
				before := wakeIdentityRows(t, f)
				require.NoError(t, refusedWakeOperation(t, f.store, key, operation))
				assert.Equal(t, before, wakeIdentityRows(t, f), "claims, holds, marks and consumed budgets remain byte-identical")
			})
		}
	}
}

func TestWakeIdentity_UnreadableJournal_PropagatesCauseWithoutMutation(t *testing.T) {
	for _, operation := range []string{"claim", "accounted claim", "hold", "advance"} {
		t.Run(operation, func(t *testing.T) {
			f := newWakeReplaceState(t, "above", false)
			key := replaceWakeIdentityKey(t, f)
			_, err := f.raw.Exec(`DROP TABLE sync_events`)
			require.NoError(t, err)
			before := wakeIdentityRows(t, f)
			err = refusedWakeOperation(t, f.store, key, operation)
			require.ErrorContains(t, err, "read wake event identity")
			var cause interface{ Code() int }
			require.ErrorAs(t, err, &cause, "the underlying SQLite error remains in the chain")
			assert.Equal(t, before, wakeIdentityRows(t, f))
		})
	}
}
