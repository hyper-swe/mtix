// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// The pull lists the local tasks it moved (MTIX-112.17.16.39): a hub create
// that takes the number of a local task with no create in the log moves that
// task, and the pull prints one line per moved task, so an agent that holds
// the old number does not act on the teammate's task.

func movesTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.New(filepath.Join(t.TempDir(), "moves.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func movesNode(id, parent, title string) *model.Node {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	return &model.Node{
		ID: id, ParentID: parent, Project: "MTIX", Title: title, Status: model.StatusOpen,
		NodeType: model.NodeTypeIssue, Priority: model.PriorityMedium, Weight: 1,
		CreatedAt: now, UpdatedAt: now, ContentHash: "h",
	}
}

func hubCreateOf(t *testing.T, id, uid, title string) *model.SyncEvent {
	t.Helper()
	payload, err := model.EncodePayload(&model.CreateNodePayload{Title: title, NodeType: model.NodeTypeEpic})
	require.NoError(t, err)
	return &model.SyncEvent{
		EventID: uid, UID: uid, ProjectPrefix: "MTIX", NodeID: id, OpType: model.OpCreateNode,
		Payload: payload, WallClockTS: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), LamportClock: 9,
		VectorClock: model.VectorClock{"peer-b": 9}, AuthorID: "peer-b", AuthorMachineHash: "bbbbbbbbbbbbbbbb",
	}
}

func TestReportPullMoves_HubCreateTookLocalTaskNumber_PrintsOneLinePerMovedTask(t *testing.T) {
	ctx := context.Background()
	s := movesTestStore(t)
	require.NoError(t, s.CreateNode(ctx, movesNode("MTIX-1", "", "local epic")))
	require.NoError(t, s.CreateNode(ctx, movesNode("MTIX-1.1", "MTIX-1", "local child")))
	_, err := s.WriteDB().ExecContext(ctx, `DELETE FROM sync_events WHERE op_type = 'create_node'`)
	require.NoError(t, err)

	var out, errOut bytes.Buffer
	mark, ok := pullMoveMark(ctx, &errOut, s)
	require.True(t, ok)
	reportPullMoves(ctx, syncPullInput{stdout: &out, stderr: &errOut}, s, mark)
	require.Empty(t, out.String(), "a pull that moved nothing prints nothing")

	uid := clock.MustNewEventID()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return s.IdempotentApply(ctx, tx, hubCreateOf(t, "MTIX-1", uid, "hub epic"))
	}))
	reportPullMoves(ctx, syncPullInput{stdout: &out, stderr: &errOut}, s, mark)

	require.Empty(t, errOut.String())
	lines := bytes.Split(bytes.TrimSpace(out.Bytes()), []byte("\n"))
	require.Len(t, lines, 2, "one line per moved task, the child included: %s", out.String())
	require.Contains(t, string(lines[0]), "renumbered: MTIX-1 -> MTIX-2 local epic")
	require.Contains(t, string(lines[1]), "renumbered: MTIX-1.1 -> MTIX-2.1 local child")

	out.Reset()
	reportPullMoves(ctx, syncPullInput{stdout: &out, stderr: &errOut}, s, mark+1_000_000)
	require.Empty(t, out.String(), "a mark past the history lists nothing")
}

// movedPullFixture is a project with a local task X-1 and child X-1.1 written
// with no create in the log, and the pull phases of a hub whose create of X-1
// is applied in place of the hub contact, so the real runSyncPullObserved runs
// around it.
func movedPullFixture(t *testing.T) (observer pullObserver, uids map[string]string) {
	t.Helper()
	_ = daemonOutcomeFixture(t)
	ctx := context.Background()
	local := movesNode("X-1", "", "local epic")
	local.Project = "X"
	child := movesNode("X-1.1", "X-1", "local child")
	child.Project = "X"
	require.NoError(t, app.store.CreateNode(ctx, local))
	require.NoError(t, app.store.CreateNode(ctx, child))
	_, err := app.store.WriteDB().ExecContext(ctx, `DELETE FROM sync_events WHERE op_type = 'create_node'`)
	require.NoError(t, err)
	uids = map[string]string{}
	for _, id := range []string{"X-1", "X-1.1"} {
		n, getErr := app.store.GetNode(ctx, id)
		require.NoError(t, getErr)
		uids[id] = n.UID
	}
	hubUID := clock.MustNewEventID()
	observer.phases = func(ctx context.Context, _ syncPullInput, _ pullIngest, _ pullObserver) (pullOutcome, bool, error) {
		ev := hubCreateOf(t, "X-1", hubUID, "hub epic")
		ev.ProjectPrefix = "X"
		err := app.store.WithTx(ctx, func(tx *sql.Tx) error { return app.store.IdempotentApply(ctx, tx, ev) })
		return pullOutcome{pulled: 1, batches: 1}, false, err
	}
	return observer, uids
}

// TestRunSyncPullObserved_HubCreateTookLocalTaskNumber_PrintsRenumberedLines:
// the output of the real pull entry point lists the moved tasks; the mark is
// taken before the pull's phases and read after them.
func TestRunSyncPullObserved_HubCreateTookLocalTaskNumber_PrintsRenumberedLines(t *testing.T) {
	observer, _ := movedPullFixture(t)
	var stdout, stderr bytes.Buffer

	require.NoError(t, runSyncPullObserved(t.Context(), syncPullInput{stdout: &stdout, stderr: &stderr, limit: 100}, observer))

	require.Contains(t, stdout.String(), "pull complete: 1 events applied")
	require.Contains(t, stdout.String(), "renumbered: X-1 -> X-2 local epic")
	require.Contains(t, stdout.String(), "renumbered: X-1.1 -> X-2.1 local child")
	require.Empty(t, stderr.String())
}

// TestRunSyncPullObserved_DaemonInput_LogsOneLinePerMovedTask: a daemon pull
// prints nothing, so each moved task is logged with its old and new number and
// its uid.
func TestRunSyncPullObserved_DaemonInput_LogsOneLinePerMovedTask(t *testing.T) {
	observer, uids := movedPullFixture(t)
	var logs bytes.Buffer
	app.logger = slog.New(slog.NewTextHandler(&logs, nil))
	var stderr bytes.Buffer
	req := daemonPullInput(&stderr, nil, transport.Options{}, nil)

	require.NoError(t, runSyncPullObserved(t.Context(), req, observer))

	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	require.Len(t, lines, 2, "one log line per moved task: %s", logs.String())
	require.Contains(t, lines[0], "old_number=X-1 new_number=X-2 uid="+uids["X-1"])
	require.Contains(t, lines[1], "old_number=X-1.1 new_number=X-2.1 uid="+uids["X-1.1"])
}

// TestLogPullMoves_NilLogger_LogsNothing: no logger is not an error.
func TestLogPullMoves_NilLogger_LogsNothing(t *testing.T) {
	require.NotPanics(t, func() { logPullMoves(nil)([]sqlite.PullMove{{OldID: "X-1", NewID: "X-2"}}) })
}
