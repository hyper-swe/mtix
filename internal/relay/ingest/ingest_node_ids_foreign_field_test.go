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

// TestRelayIngest_MistypedForeignField_CannotHideInvalidNodeID: a relay
// record whose payload carries a mistyped field of another op is still
// checked for the id its own op names, and refused when that id is outside
// the grammar (MTIX-187).
func TestRelayIngest_MistypedForeignField_CannotHideInvalidNodeID(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()

	good := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "before"})
	bad := event(t, model.OpLinkDep, "PROJ-1", 2, map[string]any{
		"depends_on_node_id": "PROJ-9é", "dep_type": "blocks", "parent_id": 5,
	})
	r.publish(t, fleetKey, marshalEvents(t, good, bad)...)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)

	require.Equal(t, 1, stats.Applied)
	require.Equal(t, 1, stats.Quarantined)
	require.Len(t, stats.Quarantines, 1)
	require.Contains(t, stats.Quarantines[0].Reason, "invalid node id")
	require.Contains(t, stats.Quarantines[0].Reason, `PROJ-9\u00e9`)
	require.NotContains(t, stats.Quarantines[0].Reason, "é", "printable non-ASCII is escaped, never raw")
}
