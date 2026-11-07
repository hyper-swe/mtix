// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// These tests apply real sync events; the named identity/sequence fault cases
// corrupt journal positions to exercise the durable stream witness.
func TestCloneWakeBootstrap_WitnessEncoding_RetainsStoredForm(t *testing.T) {
	for _, tt := range []struct {
		name     string
		position cloneWakePosition
		want     string
	}{
		{"ordinary", cloneWakePosition{generation: "0", tail: 2, eventID: "event-id"}, `"0"/2/"event-id"`},
		{"escaped", cloneWakePosition{generation: "g/x", tail: 12, eventID: "a\"/\n"}, `"g/x"/12/"a\"/\n"`},
		{"empty", cloneWakePosition{generation: "0"}, `"0"/0/""`},
	} {
		t.Run(tt.name, func(t *testing.T) { assert.Equal(t, tt.want, tt.position.witness()) })
	}
}

func cloneBootstrapFixture(t *testing.T, harness string) (*Store, *sql.DB) {
	t.Helper()
	s, raw := applyTestStore(t)
	_, err := s.RegisterSessionAtomic(t.Context(), RegistrationKey{AgentID: "worker", HostID: "host", UID: "1000"},
		func(_ *Registration, tail int64) (*Registration, error) {
			return &Registration{AgentID: "worker", HostID: "host", UID: "1000", Harness: harness,
				Epoch: 1, HighWater: tail, RegisteredAt: "2026-11-06T00:00:00Z", LastSeen: "2026-11-06T00:00:00Z"}, nil
		})
	require.NoError(t, err)
	require.NoError(t, s.AdvanceWakeScanCursor(t.Context(), "host", "1000", 0, time.Minute))
	_, err = s.AcquireWakeLease(t.Context(), WakeLeaseKey{AgentID: "worker", HostID: "host", UID: "1000"}, "", time.Minute)
	require.NoError(t, err)
	return s, raw
}

func cloneBootstrapEvent(t *testing.T, node string, lamport int64) *model.SyncEvent {
	t.Helper()
	e := foreignCreate(t, node, lamport)
	e.ProjectPrefix = "TEST"
	return e
}

func applyCloneBootstrapEvent(t *testing.T, s *Store, e *model.SyncEvent) error {
	t.Helper()
	return s.WithCloneWakeBootstrap(t.Context(), func(tx *sql.Tx) error {
		return s.IdempotentApply(t.Context(), tx, e)
	})
}

func cloneBootstrapMarks(t *testing.T, raw *sql.DB) []int64 {
	t.Helper()
	var hook, cursor, reg, lease int64
	require.NoError(t, raw.QueryRow(`SELECT
		COALESCE((SELECT MAX(cursor) FROM hook_dispatch_cursor),0),
		COALESCE((SELECT MAX(cursor) FROM wake_scan_cursor),0),
		COALESCE((SELECT MAX(high_water) FROM agent_registrations),0),
		COALESCE((SELECT MAX(high_water) FROM wake_leases),0)`).Scan(&hook, &cursor, &reg, &lease))
	return []int64{hook, cursor, reg, lease}
}

func cloneBootstrapWitness(t *testing.T, raw *sql.DB) string {
	t.Helper()
	var witness string
	require.NoError(t, raw.QueryRow(`SELECT COALESCE((SELECT value FROM meta WHERE key=?),'')`, cloneWakeBootstrapKey).Scan(&witness))
	return witness
}

func TestCloneWakeBootstrap_ContiguousBatches_SeedBothHarnessesAndRetire(t *testing.T) {
	for _, harness := range []string{"pi", "claude"} {
		t.Run(harness, func(t *testing.T) {
			s, raw := cloneBootstrapFixture(t, harness)
			for i, node := range []string{"TEST-1", "TEST-2"} {
				require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, node, int64(i+1))))
				mark := int64(i + 1)
				assert.Equal(t, []int64{mark, mark, mark, mark}, cloneBootstrapMarks(t, raw))
				require.NotEmpty(t, cloneBootstrapWitness(t, raw))
			}
			require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error { return CompleteCloneWakeBootstrap(t.Context(), tx) }))
			require.Empty(t, cloneBootstrapWitness(t, raw))
			require.NoError(t, applyOnce(t, s, cloneBootstrapEvent(t, "TEST-3", 3)))
			require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, "TEST-4", 4)))
			assert.Equal(t, []int64{2, 2, 2, 2}, cloneBootstrapMarks(t, raw), "completion prevents a later resume from covering fresh mail")
			assert.Empty(t, cloneBootstrapWitness(t, raw))
		})
	}
}

func TestCloneWakeBootstrap_InterruptedStream_InvalidationPersists(t *testing.T) {
	for _, fault := range []string{"outside-write", "tail-identity-fault", "tail-sequence-fault", "discard-generation"} {
		t.Run(fault, func(t *testing.T) {
			s, raw := cloneBootstrapFixture(t, "claude")
			first := cloneBootstrapEvent(t, "TEST-1", 1)
			require.NoError(t, applyCloneBootstrapEvent(t, s, first))
			switch fault {
			case "outside-write":
				require.NoError(t, applyOnce(t, s, cloneBootstrapEvent(t, "TEST-2", 2)))
			case "tail-identity-fault":
				require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, "TEST-2", 2)))
				other := cloneBootstrapEvent(t, "TEST-3", 3)
				_, err := raw.Exec(`UPDATE sync_events SET event_id=? WHERE rowid=?`, other.EventID, 2)
				require.NoError(t, err)
				var firstID string
				require.NoError(t, raw.QueryRow(`SELECT event_id FROM sync_events WHERE rowid=1`).Scan(&firstID))
				require.Equal(t, first.EventID, firstID, "only the tail identity changed")
			case "tail-sequence-fault":
				_, err := raw.Exec(`UPDATE sync_events SET rowid=? WHERE event_id=?`, 3, first.EventID)
				require.NoError(t, err)
			case "discard-generation":
				require.NoError(t, DiscardLocal(t.Context(), s, t.TempDir()))
				require.NoError(t, applyOnce(t, s, first)) // same seq and identity, different real generation
			}
			before := cloneBootstrapMarks(t, raw)
			for i, node := range []string{"TEST-3", "TEST-4"} {
				require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, node, int64(i+3))))
				assert.Equal(t, before, cloneBootstrapMarks(t, raw))
				assert.Empty(t, cloneBootstrapWitness(t, raw), "later batches cannot recreate invalidated intent")
			}
		})
	}
}

func TestCloneWakeBootstrap_NonemptyWithoutWitness_IsNeverBootstrap(t *testing.T) {
	s, raw := cloneBootstrapFixture(t, "pi")
	require.NoError(t, applyOnce(t, s, cloneBootstrapEvent(t, "TEST-1", 1)))
	require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, "TEST-2", 2)))
	assert.Equal(t, []int64{0, 0, 0, 0}, cloneBootstrapMarks(t, raw))
	assert.Empty(t, cloneBootstrapWitness(t, raw))
}

func TestCloneWakeBootstrap_ReadPoolUnavailable_UsesWriter(t *testing.T) {
	s, raw := cloneBootstrapFixture(t, "pi")
	require.NoError(t, s.ReadDB().Close())
	require.NoError(t, applyCloneBootstrapEvent(t, s, cloneBootstrapEvent(t, "TEST-1", 1)))
	assert.Equal(t, []int64{1, 1, 1, 1}, cloneBootstrapMarks(t, raw))
}
