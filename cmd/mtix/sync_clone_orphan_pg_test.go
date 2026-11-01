// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Real-Postgres test of a clone of a hub that holds events pushed under an
// old number (MTIX-95.37.1). Gated on MTIX_PG_TEST_DSN.

// TestRunSyncClone_HubHoldsOldNumberDependents_SucceedsAndQuarantinesThem:
// the hub holds a creation and, as a client before 0.5.4 pushed them, a
// claim and a child create of a task the hub renumbered, under the old
// number. The clone succeeds, applies the creation, quarantines both
// dependents (source pull, clone origin), names them, and doctor then fails
// with the acknowledge command and passes once they are acknowledged.
func TestRunSyncClone_HubHoldsOldNumberDependents_SucceedsAndQuarantinesThem(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-4", "", 0, 4, "ordinary")))
	pending, err := readPendingBatch(ctx, f.b, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	claim := orphanClaim(t, "TEST-9.1", pending[0].LamportClock+1)
	child := orphanClaim(t, "TEST-9.1.1", pending[0].LamportClock+2)
	accepted, _, _, err := f.pool.PushEventsWithRenumbers(ctx, []*model.SyncEvent{pending[0], claim, child})
	require.NoError(t, err)
	require.Len(t, accepted, 3, "precondition: the hub holds the old-number events")
	var stdout, stderr bytes.Buffer

	err = runSyncClone(ctx, &stdout, &stderr, nil, transport.Options{InsecureTLS: true}, false, 2)

	require.NoError(t, err, stderr.String())
	require.Contains(t, stdout.String(), "2 orphaned events quarantined")
	require.Contains(t, stderr.String(), claim.EventID)
	require.True(t, f.appliedOnPeer(t, pending[0].EventID))
	require.False(t, f.appliedOnPeer(t, claim.EventID))
	require.Len(t, quarantined(t), 2)
	ok, detail := checkQuarantinedEvents(ctx, app.store)
	require.False(t, ok)
	require.Contains(t, detail, "2 of them clone orphans")
}
