// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeRegistrationMark_MissingRow_ReportsNotFound(t *testing.T) {
	s := newTestStore(t)
	mark, err := s.WakeRegistrationMark(t.Context(), model.WakeKey{AgentID: "a", HostID: "h", UID: "u"})
	require.NoError(t, err)
	require.False(t, mark.Found)
}

func TestSetWakeHold_AdvancesMonotonicallyAndGuardsEpoch(t *testing.T) {
	s := newTestStore(t)
	_, err := s.WriteDB().ExecContext(t.Context(), `INSERT INTO agent_registrations
		(agent_id,harness,uid,host_id,origin,epoch,high_water,pending_hold,registered_at,last_seen,registrar_version,attach_protocol)
		VALUES ('a','claude','u','h','manual',2,10,0,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','t',1)`)
	require.NoError(t, err)
	key := model.WakeKey{AgentID: "a", HostID: "h", UID: "u", Epoch: 2}
	require.NoError(t, s.SetWakeHold(t.Context(), key, 0, true))
	mark, err := s.WakeRegistrationMark(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, model.WakeRegistrationMark{Found: true, Epoch: 2, HighWater: 10, Held: true}, mark)
	require.NoError(t, s.SetWakeHold(t.Context(), key, 4, false))
	mark, _ = s.WakeRegistrationMark(t.Context(), key)
	require.Equal(t, int64(10), mark.HighWater, "the mark never moves back")
	require.False(t, mark.Held)
	require.NoError(t, s.SetWakeHold(t.Context(), key, 12, false))
	mark, _ = s.WakeRegistrationMark(t.Context(), key)
	require.Equal(t, int64(12), mark.HighWater)
	stale := key
	stale.Epoch = 1
	require.NoError(t, s.SetWakeHold(t.Context(), stale, 99, true))
	mark, _ = s.WakeRegistrationMark(t.Context(), key)
	require.Equal(t, int64(12), mark.HighWater)
	require.False(t, mark.Held, "a stale epoch cannot change a re-registered row")
}
