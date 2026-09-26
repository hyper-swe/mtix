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
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestPushLoop_HeldCreationWhoseUIDNoNodeHas_RealHub_TeammateTaskIntact is
// the review r1 S1 and S2 reproduction against a real hub (MTIX-95.12 run
// 2): A's creation of TEST-1 is size-held; B creates and pushes its own
// TEST-1; A's task then loses the uid its creation carries (a merge import
// adopts the file's uid), or its creation was queued without one; A
// retitles TEST-1, creates a child under it and pushes; B pulls. B's task
// must keep its title, get no child, and no A event for TEST-1 or its
// subtree may reach the hub.
func TestPushLoop_HeldCreationWhoseUIDNoNodeHas_RealHub_TeammateTaskIntact(t *testing.T) {
	tests := []struct {
		name    string
		loseUID func(t *testing.T)
	}{
		{"uid adopted by a merge", func(t *testing.T) { adoptFileUID(t, "TEST-1") }},
		{"creation queued without a uid", func(t *testing.T) { queueWithoutUID(t, "TEST-1") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := openCmdHub(t)
			ctx := context.Background()
			var stderr bytes.Buffer
			t.Setenv(sqlite.AuthorIDEnv, "agent-a")
			initTestApp(t)
			appA := app
			require.NoError(t, runCreate("A's task", "", "", 3, "", overWireCap(), "", "", ""))
			_, _, _, _, err := pushLoop(ctx, &stderr, pool, app.store)
			require.NoError(t, err)

			t.Setenv(sqlite.AuthorIDEnv, "agent-b")
			initTestApp(t)
			appB := app
			require.NoError(t, runCreate("B's own task", "", "", 3, "", "", "", "", ""))
			_, _, _, _, err = pushLoop(ctx, &stderr, pool, app.store)
			require.NoError(t, err)

			app = appA
			t.Setenv(sqlite.AuthorIDEnv, "agent-a")
			tt.loseUID(t)
			require.NoError(t, runUpdate("TEST-1", "A retitled its task", "", "", "", 0, "", ""))
			require.NoError(t, runCreate("A's child", "TEST-1", "", 3, "", "", "", "", ""))
			_, _, _, _, err = pushLoop(ctx, &stderr, pool, app.store)
			require.NoError(t, err)

			app = appB
			_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
			require.NoError(t, err)
			node, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			require.Equal(t, "B's own task", node.Title)
			require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM nodes WHERE parent_id = 'TEST-1'`),
				"B's task gets no child from A")
			var fromA int
			require.NoError(t, pool.Inner().QueryRow(ctx,
				`SELECT COUNT(*) FROM sync_events WHERE (node_id = 'TEST-1' OR node_id LIKE 'TEST-1.%')
				 AND author_id = 'agent-a'`).Scan(&fromA))
			require.Zero(t, fromA, "no event of A's held task or its subtree reaches the hub")
			app = appA
			requireHeld := func(node string, op model.OpType) {
				_, held := quarantined(t)[eventIDFor(t, node, op)]
				require.True(t, held, "A's %s %s is held", node, op)
			}
			requireHeld("TEST-1", model.OpUpdateField)
			requireHeld("TEST-1.1", model.OpCreateNode)
		})
	}
}
