// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Retained-owner write failures leave the pending event and prior mark intact.
package sqlite

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleaseWakeLease_RearmWriteFails_KeepsOwnerAndPending(t *testing.T) {
	s := newInternalTestStore(t)
	lease := internalWakeLease(t, s)
	_, err := s.writeDB.Exec(`INSERT INTO sync_events
 (event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
 VALUES('event','TEST','TEST-1','comment','{"to":"TEST-agent","text":"Pending"}',0,0,'{}','sender','host','2026-10-11T00:00:00Z')`)
	require.NoError(t, err)
	_, err = s.writeDB.Exec(`CREATE TRIGGER reject_wake_rearm BEFORE UPDATE ON wake_leases BEGIN SELECT RAISE(ABORT,'owned rearm fault'); END;`)
	require.NoError(t, err)
	_, err = s.ReleaseWakeLease(t.Context(), internalWakeKey(), lease.Holder, time.Minute, false)
	require.ErrorContains(t, err, "owned rearm fault")
	var holder string
	var mark int64
	require.NoError(t, s.readDB.QueryRow("SELECT holder,high_water FROM wake_leases").Scan(&holder, &mark))
	assert.Equal(t, lease.Holder, holder)
	assert.Equal(t, lease.HighWater, mark)
	inbox, err := s.InboxList(t.Context(), internalWakeKey().AgentID)
	require.NoError(t, err)
	assert.Len(t, inbox, 1)
}
