// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// commitFromAnotherConnection commits an unrelated change to the board from a
// second connection, as a pull applying a backlog or another agent does.
func commitFromAnotherConnection(t *testing.T) {
	t.Helper()
	other, err := sqlite.New(filepath.Join(app.mtixDir, "data"), app.logger)
	require.NoError(t, err)
	_, err = other.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET updated_at = updated_at WHERE id = 'TEST-2'`)
	require.NoError(t, err)
	require.NoError(t, other.Close())
}

// TestPushLoop_WriterCommitsBeforeEveryRead_PushStillCompletes (MTIX-95.39):
// another connection commits just before every read of a batch's tasks, so
// the data version never stays put across a decision. Push used to give up
// after 16 reads ("the store kept changing ... push again"); it now decides
// the last attempt under the write lock, where no commit can intervene, so
// it completes, holds exactly what the subtree rule holds, and sends the
// rest. No timing is involved: the writer runs on every read.
func TestPushLoop_WriterCommitsBeforeEveryRead_PushStillCompletes(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	otherEdits(t, "TEST-2", pushBatchSize)
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-1.1", "child edit", "", "", "", 0, "", ""))
	calls := 0
	ctx := context.WithValue(context.Background(), pushSubjectsHookKey{}, func() {
		calls++
		commitFromAnotherConnection(t)
	})

	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, hub, app.store)

	require.NoError(t, err, "push completes though a writer commits before every read")
	require.GreaterOrEqual(t, calls, maxIndexReads-1, "the writer ran before every read, up to the locked attempt")
	requireHeldDependent(t, hub, "TEST-1.1", model.OpCreateNode, root)
	requireHeldDependent(t, hub, "TEST-1.1", model.OpUpdateField, eventIDFor(t, "TEST-1.1", model.OpCreateNode))
	require.NotContains(t, stderr.String(), "kept changing")
}

// TestHeldPushCreations_ReadsOnlyHeldCreationsWithoutPayloads (MTIX-95.39):
// the reread of the held-creation index reads only create_node holds and no
// payload, so its cost does not grow with the payloads held.
func TestHeldPushCreations_ReadsOnlyHeldCreationsWithoutPayloads(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, runUpdate("TEST-1", "edit of the held task", "", "", "", 0, "", ""))
	require.NoError(t, pushWith(t, hub))

	all, err := app.store.HeldPushEvents(context.Background(), -1)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(all), 2, "the creation and the dependent edit are held")
	require.NotEmpty(t, all[0].Payload, "the full read carries payloads")

	creations, err := app.store.HeldPushCreations(context.Background())
	require.NoError(t, err)
	require.Len(t, creations, 1, "only the held creation")
	require.Equal(t, string(model.OpCreateNode), creations[0].OpType)
	require.Empty(t, creations[0].Payload, "no payload is read")
	require.Equal(t, all[0].EventID, creations[0].EventID)
	require.Equal(t, all[0].TaskNodeID, creations[0].TaskNodeID)
}

// requireHeldUnder is requireHeldDependent for a board with many holds: it
// reads the one hold by event id, since the listing helper is capped.
func requireHeldUnder(t *testing.T, hub *fakePushHub, node string, op model.OpType, rootEventID string) {
	t.Helper()
	id := eventIDFor(t, node, op)
	var reason string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT reason FROM sync_quarantine WHERE event_id = ? AND source = 'push'`, id).Scan(&reason),
		"%s %s is held", node, op)
	require.True(t, strings.HasPrefix(reason, "depends on held create of "+rootEventID), reason)
	require.False(t, hub.sent(id), "%s %s is never sent", node, op)
	require.Equal(t, "pending", syncStatusOf(t, id))
}

// seedHeldCreations adds n held push creations to the board with two bulk
// statements in one transaction, not through the CLI (MTIX-95.39): a
// create_node row in sync_events for each, held in sync_quarantine under a
// permanent reason. They stand for a large backlog of held tasks.
func seedHeldCreations(t *testing.T, n int) {
	t.Helper()
	err := app.store.WithTx(context.Background(), func(tx *sql.Tx) error {
		// n create_node events for tasks HELD-1 .. HELD-n, uid = event id, with
		// a payload of a few hundred bytes each and Lamport clocks 1 .. n.
		if _, err := tx.ExecContext(context.Background(), `
			WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?)
			INSERT INTO sync_events (event_id, project_prefix, node_id, op_type, payload, wall_clock_ts,
			    lamport_clock, vector_clock, author_id, author_machine_hash, sync_status, created_at, uid)
			SELECT 'held-' || i, 'TEST', 'HELD-' || i, 'create_node', '{"title":"' || printf('%0300d', i) || '"}',
			    1, i, '{}', 'seed', 'seed', 'pending', '2026-01-01T00:00:00Z', 'held-' || i
			FROM seq`, n); err != nil {
			return err
		}
		// Each of them held by a permanent reason.
		_, err := tx.ExecContext(context.Background(), `
			WITH RECURSIVE seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?)
			INSERT INTO sync_quarantine (event_id, source, raw_event, reason, first_seen, last_attempt, attempts, cli_version)
			SELECT 'held-' || i, 'push', '{}', 'too large: seeded for the reread test', '2026-01-01T00:00:00Z',
			    '2026-01-01T00:00:00Z', 1, 'test'
			FROM seq`, n)
		return err
	})
	require.NoError(t, err)
}

// TestPushLoop_ManyHeldCreationsAndAWriter_PushStillCompletes (MTIX-95.39):
// with 501 and with 3001 held creations on the board and a writer
// committing before every read, push completes and holds what the subtree
// rule holds. The sizes are the ticket's probe sizes; the backlog is seeded
// in bulk and the writer runs on every read, so the case is deterministic
// and takes a fraction of a second.
func TestPushLoop_ManyHeldCreationsAndAWriter_PushStillCompletes(t *testing.T) {
	for _, held := range []int{501, 3001} {
		t.Run(fmt.Sprintf("%d held", held), func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
			require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
			root := eventIDFor(t, "TEST-1", model.OpCreateNode)
			hub := newFakePushHub()
			require.NoError(t, pushWith(t, hub))
			otherEdits(t, "TEST-2", pushBatchSize)
			require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
			require.NoError(t, runUpdate("TEST-1.1", "child edit", "", "", "", 0, "", ""))
			seedHeldCreations(t, held)
			before := pushHoldCount(t)
			calls := 0
			ctx := context.WithValue(context.Background(), pushSubjectsHookKey{}, func() {
				calls++
				commitFromAnotherConnection(t)
			})

			var stderr bytes.Buffer
			_, err := pushLoop(ctx, &stderr, hub, app.store)

			require.NoError(t, err)
			require.GreaterOrEqual(t, calls, maxIndexReads-1)
			requireHeldUnder(t, hub, "TEST-1.1", model.OpCreateNode, root)
			requireHeldUnder(t, hub, "TEST-1.1", model.OpUpdateField, eventIDFor(t, "TEST-1.1", model.OpCreateNode))
			require.Equal(t, before+2, pushHoldCount(t), "the seeded holds stay and the child's two events join them")
			for _, ids := range hub.calls {
				for _, id := range ids {
					require.False(t, strings.HasPrefix(id, "held-"), "no seeded held event is ever sent")
				}
			}
		})
	}
}
