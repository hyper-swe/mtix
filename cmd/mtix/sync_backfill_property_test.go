// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestProp_BackfillConvergence is the property test for MTIX-15.13.1:
// after backfill, every node in the canonical `nodes` table must have
// exactly one matching `create_node` event in `sync_events`. No
// orphan events, no missing events, no duplicates.
//
// 100 random setups (varying node counts, statuses, descriptions,
// prompts, acceptances). The seed is deterministic so failures
// reproduce.
func TestProp_BackfillConvergence(t *testing.T) {
	if testing.Short() {
		t.Skip("property test skipped under -short")
	}
	const iterations = 100
	const minNodes, maxNodes = 1, 20

	for iter := 0; iter < iterations; iter++ {
		iter := iter
		t.Run(fmt.Sprintf("iter-%03d", iter), func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			r := rand.New(rand.NewSource(int64(iter) + 1))
			n := minNodes + r.Intn(maxNodes-minNodes+1)

			for i := 0; i < n; i++ {
				desc := ""
				if r.Intn(2) == 0 {
					desc = fmt.Sprintf("description-%d-%d", iter, i)
				}
				prompt := ""
				if r.Intn(2) == 0 {
					prompt = fmt.Sprintf("prompt-%d", i)
				}
				acceptance := ""
				if r.Intn(3) == 0 {
					acceptance = "acceptance"
				}
				priority := 1 + r.Intn(5)
				// runCreate signature: title, under, nodeType, priority,
				// description, prompt, acceptance, labels, assign.
				require.NoError(t, runCreate(
					fmt.Sprintf("node-%d-%d", iter, i),
					"", "", priority,
					desc, prompt, acceptance, "", ""),
					"create node %d on iter %d", i, iter)
			}

			// Reset to upgrader-simulated state.
			wipeSyncEvents(t)

			// Backfill should succeed and emit exactly n create_node events.
			_, err := app.store.Backfill(ctx, false)
			require.NoError(t, err)

			createCount := countSyncEventsByOp(t, model.OpCreateNode)
			require.Equal(t, n, createCount,
				"iter %d: expected %d create_node events, got %d",
				iter, n, createCount)

			// Every node in nodes must be referenced by exactly one
			// create_node event.
			var dupCount int
			require.NoError(t, app.store.QueryRow(ctx, `
				SELECT count(*) FROM (
				  SELECT node_id FROM sync_events
				  WHERE op_type = 'create_node'
				  GROUP BY node_id
				  HAVING count(*) > 1
				)`).Scan(&dupCount))
			require.Equal(t, 0, dupCount,
				"iter %d: %d node(s) have multiple create_node events", iter, dupCount)

			// No orphan events (event refers to a node_id not in nodes).
			var orphanCount int
			require.NoError(t, app.store.QueryRow(ctx, `
				SELECT count(*) FROM sync_events e
				WHERE op_type = 'create_node'
				  AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.id = e.node_id)`,
			).Scan(&orphanCount))
			require.Equal(t, 0, orphanCount,
				"iter %d: %d orphan create_node event(s)", iter, orphanCount)
		})
	}
}
