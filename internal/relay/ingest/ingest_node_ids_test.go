// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
)

// TestRelayIngest_InvalidNodeID_QuarantinedNotApplied: a relay record whose
// node id is outside the node id grammar is refused like any other record
// that fails the envelope (MTIX-187): skipped loudly from an authenticated
// peer, never applied, and the records around it still apply.
func TestRelayIngest_InvalidNodeID_QuarantinedNotApplied(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()

	good1 := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "before"})
	bad := event(t, model.OpCreateNode, "PROJ-1é.4", 2,
		&model.CreateNodePayload{Title: "outside the grammar", ParentID: "PROJ-1"})
	good2 := event(t, model.OpCreateNode, "PROJ-2", 3, &model.CreateNodePayload{Title: "after"})
	r.publish(t, fleetKey, marshalEvents(t, good1, bad, good2)...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)

	require.Equal(t, 2, stats.Applied, "the records around it still apply")
	require.Equal(t, 1, stats.Quarantined)
	require.Len(t, stats.Quarantines, 1)
	require.Contains(t, stats.Quarantines[0].Reason, "invalid node id")
	require.Contains(t, stats.Quarantines[0].Reason, "PROJ-1é.4")
	_, err = r.store.GetNode(ctx, "PROJ-1é.4")
	require.ErrorIs(t, err, model.ErrNotFound, "the record was not applied")
}
