// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// holdFixture wires a dispatcher over a store holding one registration for the key.
type holdFixture struct {
	store *sqlite.Store
	d     *service.WakeDispatcher
	key   model.WakeKey
	sent  []int64
	hops  map[int64]int64
}

type hopTableStore struct {
	*sqlite.Store
	hops map[int64]int64
}

func (s hopTableStore) WakeEventHop(_ context.Context, key model.WakeKey) (int64, error) {
	return s.hops[key.EventSeq], nil
}

func newHoldFixture(t *testing.T, limit string, highWater int64) *holdFixture {
	t.Helper()
	store, _, alerts := wakeServiceStore(t)
	state := testutil.OperatorState(t)
	_, err := wakeConfig(t, state, "", true).Set("wake.max_hop_depth", limit)
	require.NoError(t, err)
	f := &holdFixture{store: store, key: wakeServiceKey(), hops: map[int64]int64{}}
	_, err = store.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES (?,?,?,?,?,?,?,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`,
		f.key.AgentID, "claude", f.key.UID, f.key.HostID, "manual", f.key.Epoch, highWater)
	require.NoError(t, err)
	f.d, err = service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: hopTableStore{store, f.hops}, Alerts: alerts,
		Settings: state, BudgetState: state, Clock: time.Now, Held: func(string) {},
		Send: func(_ context.Context, a model.WakeAttempt) error {
			f.sent = append(f.sent, a.Key.EventSeq)
			return nil
		}})
	require.NoError(t, err)
	return f
}

func (f *holdFixture) at(seq int64) model.WakeKey {
	k := f.key
	k.EventSeq = seq
	return k
}

func (f *holdFixture) registration(t *testing.T) (int64, bool) {
	t.Helper()
	var hw, hold int64
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(),
		"SELECT high_water, pending_hold FROM agent_registrations WHERE agent_id=?", f.key.AgentID).Scan(&hw, &hold))
	return hw, hold == 1
}

func TestHold_Lifted_ReArmsFromOwnHighWater(t *testing.T) {
	state := testutil.OperatorState(t)
	_, err := wakeConfig(t, state, "", true).Set("wake.max_hop_depth", "1")
	require.NoError(t, err)
	store, _, alerts := wakeServiceStore(t)
	f := &holdFixture{store: store, key: wakeServiceKey(), hops: map[int64]int64{7: 3, 8: 0}}
	_, err = store.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES (?,?,?,?,?,?,5,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`,
		f.key.AgentID, "claude", f.key.UID, f.key.HostID, "manual", f.key.Epoch)
	require.NoError(t, err)
	f.d, err = service.NewWakeDispatcher(service.WakeDispatcherOptions{Store: hopTableStore{store, f.hops}, Alerts: alerts,
		Settings: state, BudgetState: state, Clock: time.Now, Held: func(string) {},
		Send: func(_ context.Context, a model.WakeAttempt) error {
			f.sent = append(f.sent, a.Key.EventSeq)
			return nil
		}})
	require.NoError(t, err)

	// I10: an event at or below the registration's own high-water never wakes.
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(4), f.at(5)}))
	require.Empty(t, f.sent)

	// Event 7 trips the hop guard: held, high-water stays, and event 8 (same registration) waits behind it.
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(7), f.at(8)}))
	require.Empty(t, f.sent)
	hw, hold := f.registration(t)
	require.Equal(t, int64(5), hw)
	require.True(t, hold)

	// The operator loosens the guard; the first pass after that wakes the held event, then the next.
	_, err = wakeConfig(t, state, "", true).Set("wake.max_hop_depth", "3")
	require.NoError(t, err)
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(7), f.at(8)}))
	require.Equal(t, []int64{7, 8}, f.sent)
	hw, hold = f.registration(t)
	require.Equal(t, int64(8), hw)
	require.False(t, hold)

	// Replaying delivered events never wakes again (I10).
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(7), f.at(8)}))
	require.Equal(t, []int64{7, 8}, f.sent)
}

func TestHold_OtherRegistration_DoesNotMoveOwnHighWater(t *testing.T) {
	f := newHoldFixture(t, "32", 2)
	other := f.key
	other.AgentID = "other"
	_, err := f.store.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES ('other','claude',?,?,'manual',1,50,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','test',1)`, f.key.UID, f.key.HostID)
	require.NoError(t, err)
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{f.at(3)}))
	require.Equal(t, []int64{3}, f.sent, "worker wakes above its own mark although another registration is at 50")
	hw, _ := f.registration(t)
	require.Equal(t, int64(3), hw)
	var otherHW int64
	require.NoError(t, f.store.ReadDB().QueryRowContext(t.Context(), "SELECT high_water FROM agent_registrations WHERE agent_id='other'").Scan(&otherHW))
	require.Equal(t, int64(50), otherHW)
}

func TestHold_StaleEpoch_NeverWakes(t *testing.T) {
	f := newHoldFixture(t, "32", 0)
	stale := f.at(3)
	stale.Epoch = f.key.Epoch - 1
	require.NoError(t, f.d.Pass(t.Context(), []model.WakeKey{stale}))
	require.Empty(t, f.sent)
}
