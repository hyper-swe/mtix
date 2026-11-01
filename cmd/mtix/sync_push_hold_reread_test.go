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

// writerStore opens a second connection to the board, as a pull or another
// agent holds, with a short busy timeout so a write that the push's lock
// keeps out fails at once instead of waiting (MTIX-95.39).
func writerStore(t *testing.T) *sqlite.Store {
	t.Helper()
	other, err := sqlite.New(filepath.Join(app.mtixDir, "data"), app.logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, other.Close()) })
	_, err = other.WriteDB().ExecContext(context.Background(), `PRAGMA busy_timeout = 50`)
	require.NoError(t, err)
	return other
}

// commitFrom commits an unrelated change to the board from other.
func commitFrom(t *testing.T, other *sqlite.Store) {
	t.Helper()
	require.NoError(t, probeWrite(other), "the writer commits")
}

// probeWrite tries one small write from other and returns its error.
func probeWrite(other *sqlite.Store) error {
	_, err := other.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET updated_at = updated_at WHERE id = 'TEST-2'`)
	return err
}

// heldChildSetup creates a held task TEST-1 (a payload over the wire cap), a
// task TEST-2, and, after one push, a child TEST-1.1 with an edit: the two
// events the subtree rule must hold. It returns the held creation's event id.
func heldChildSetup(t *testing.T, hub *fakePushHub) string {
	t.Helper()
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-1.1", "child edit", "", "", "", 0, "", ""))
	return root
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
	hub := newFakePushHub()
	root := heldChildSetup(t, hub)
	other := writerStore(t)
	calls := 0
	ctx := context.WithValue(context.Background(), pushSubjectsHookKey{}, func() {
		calls++
		commitFrom(t, other)
	})

	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, hub, app.store)

	require.NoError(t, err, "push completes though a writer commits before every read")
	require.Equal(t, maxIndexReads-1, calls, "the writer ran before every read, up to the locked attempt")
	requireHeldDependent(t, hub, "TEST-1.1", model.OpCreateNode, root)
	requireHeldDependent(t, hub, "TEST-1.1", model.OpUpdateField, eventIDFor(t, "TEST-1.1", model.OpCreateNode))
	require.NotContains(t, stderr.String(), "kept changing")
}

// TestPushLoop_RenumberCommittedBeforeLockedAttempt_DependentsStayHeld: a
// renumber of the held task's subtree committed on the last read before the
// locked attempt makes the index built before it stale. The locked attempt
// rebuilds the index inside the lock, so the child's creation and edit are
// still held and never sent. A locked attempt that decided on the older
// index would send them.
func TestPushLoop_RenumberCommittedBeforeLockedAttempt_DependentsStayHeld(t *testing.T) {
	initTestApp(t)
	hub := newFakePushHub()
	root := heldChildSetup(t, hub)
	other := writerStore(t)
	calls := 0
	ctx := context.WithValue(context.Background(), pushSubjectsHookKey{}, func() {
		calls++
		if calls < maxIndexReads-1 {
			commitFrom(t, other)
			return
		}
		require.NoError(t, other.RenumberSubtree(context.Background(), "TEST-1", 5))
	})

	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, hub, app.store)

	require.NoError(t, err)
	require.Equal(t, maxIndexReads-1, calls)
	requireHeldDependent(t, hub, "TEST-1.1", model.OpCreateNode, root)
	requireHeldDependent(t, hub, "TEST-1.1", model.OpUpdateField, eventIDFor(t, "TEST-1.1", model.OpCreateNode))
}

// TestPushLoop_LockedAttempt_KeepsOtherWritersOutAndNamesHoldsAfterCommit: a
// write from another connection fails (the database is locked) while the
// locked attempt rebuilds its index, so the decision and the holds it records
// stand on one state; and the lines naming the holds are printed only after
// the transaction has committed, when the same write succeeds.
func TestPushLoop_LockedAttempt_KeepsOtherWritersOutAndNamesHoldsAfterCommit(t *testing.T) {
	initTestApp(t)
	hub := newFakePushHub()
	heldChildSetup(t, hub)
	other := writerStore(t)
	var lockedErr, namedErr error
	named := 0
	ctx := context.WithValue(context.Background(), pushSubjectsHookKey{}, func() { commitFrom(t, other) })
	locked := 0
	ctx = context.WithValue(ctx, pushLockedHookKey{}, func() {
		locked++
		lockedErr = probeWrite(other)
	})
	stderr := &probingWriter{onLine: func() {
		named++
		namedErr = probeWrite(other)
	}}

	_, err := pushLoop(ctx, stderr, hub, app.store)

	require.NoError(t, err)
	require.Equal(t, 1, locked, "the batch's last attempt ran under the lock")
	require.Error(t, lockedErr, "no other connection can write inside the lock")
	require.Contains(t, lockedErr.Error(), "locked")
	require.Equal(t, 2, named, "the child's creation and edit are named")
	require.NoError(t, namedErr, "the holds are named after the lock is released")
}

// probingWriter calls onLine for each line starting with "push: held event".
type probingWriter struct{ onLine func() }

// Write implements io.Writer.
func (w *probingWriter) Write(p []byte) (int, error) {
	for _, line := range strings.Split(string(p), "\n") {
		if strings.HasPrefix(line, "push: held event") {
			w.onLine()
		}
	}
	return len(p), nil
}

// TestHeldIndex_HoldBatchLocked_SkipsTheLockWhenNothingIsHeld: with no
// creation held and a batch that holds nothing, the last attempt decides
// without opening the write lock, so a long write by another process cannot
// make it fail; with a creation held it takes the lock.
func TestHeldIndex_HoldBatchLocked_SkipsTheLockWhenNothingIsHeld(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("plain", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	events, err := readPendingBatch(context.Background(), app.store, pushBatchSize)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	locked := 0
	ctx := context.WithValue(context.Background(), pushLockedHookKey{}, func() { locked++ })

	idx, err := newHeldIndex(ctx, &bytes.Buffer{}, app.store)
	require.NoError(t, err)
	valid, held, err := idx.holdBatchLocked(ctx, &bytes.Buffer{}, app.store, events)
	require.NoError(t, err)
	require.Len(t, valid, len(events), "nothing is held")
	require.Zero(t, held)
	require.Zero(t, locked, "no lock was opened")

	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	events, err = readPendingBatch(context.Background(), app.store, pushBatchSize)
	require.NoError(t, err)
	idx, err = newHeldIndex(ctx, &bytes.Buffer{}, app.store)
	require.NoError(t, err)
	_, held, err = idx.holdBatchLocked(ctx, &bytes.Buffer{}, app.store, events)
	require.NoError(t, err)
	require.Equal(t, 1, held, "the oversized creation is held")
	require.Equal(t, 1, locked, "a batch that holds something decides under the lock")
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
// with 501 and with 3001 held creations on the board, the ticket's probe
// sizes, and a writer committing before every read, push completes and holds
// what the subtree rule holds. The backlog is seeded in bulk and the writer
// runs on every read, so the case is deterministic; it fails against the
// MTIX-95.12 code at both sizes.
func TestPushLoop_ManyHeldCreationsAndAWriter_PushStillCompletes(t *testing.T) {
	for _, held := range []int{501, 3001} {
		t.Run(fmt.Sprintf("%d held", held), func(t *testing.T) {
			initTestApp(t)
			hub := newFakePushHub()
			root := heldChildSetup(t, hub)
			seedHeldCreations(t, held)
			before := pushHoldCount(t)
			other := writerStore(t)
			ctx := context.WithValue(context.Background(), pushSubjectsHookKey{}, func() { commitFrom(t, other) })

			var stderr bytes.Buffer
			_, err := pushLoop(ctx, &stderr, hub, app.store)

			require.NoError(t, err)
			requireHeldUnder(t, hub, "TEST-1.1", model.OpCreateNode, root)
			requireHeldUnder(t, hub, "TEST-1.1", model.OpUpdateField, eventIDFor(t, "TEST-1.1", model.OpCreateNode))
			require.Equal(t, before+2, pushHoldCount(t), "the seeded holds stay and the child's two events join them")
			for _, ids := range hub.calls {
				for _, id := range ids {
					require.False(t, strings.HasPrefix(id, "held-"), fmt.Sprintf("seeded held event %s is never sent", id))
				}
			}
		})
	}
}
