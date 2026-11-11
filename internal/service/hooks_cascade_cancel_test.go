// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.17.16.12: a cascade cancel now journals one transition_status for
// each descendant it cancels, and hooks fire on every journaled
// transition_status, so status.changed fires once for each cancelled
// descendant (it fired once, for the root, before). The pulling side fires the
// same events.

const cancelledWakeHook = `
hooks:
  - name: cancel-watch
    match:
      events: [status.changed]
      status-to: [cancelled]
      to-agent: worker
    deliver: [inbox]
`

// seedCancelTree creates PROJ-1 with two open children and one done child.
// It returns the root id and the ids a cascade cancel changes.
func seedCancelTree(t *testing.T, svc *service.NodeService) (string, []string) {
	t.Helper()
	ctx := context.Background()
	root, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "root", Creator: "poster"})
	require.NoError(t, err)
	var open []string
	for _, title := range []string{"a", "b", "done one"} {
		child, cErr := svc.CreateNode(ctx, &service.CreateNodeRequest{
			Project: "PROJ", ParentID: root.ID, Title: title, Creator: "poster",
		})
		require.NoError(t, cErr)
		if title == "done one" {
			require.NoError(t, svc.TransitionStatus(ctx, child.ID, model.StatusInProgress, "", "poster"))
			require.NoError(t, svc.TransitionStatus(ctx, child.ID, model.StatusDone, "", "poster"))
			continue
		}
		open = append(open, child.ID)
	}
	return root.ID, open
}

// TestLedgerDispatch_CascadeCancel_FiresStatusChangedOncePerCancelledDescendant
// pins the intended hook fan-out of a cascade cancel on the cancelling machine:
// the root and each cancelled descendant fire once, and the descendant that
// was already done fires nothing.
func TestLedgerDispatch_CascadeCancel_FiresStatusChangedOncePerCancelledDescendant(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, cancelledWakeHook)
	rootID, _ := seedCancelTree(t, svc)

	require.NoError(t, svc.CancelNode(ctx, rootID, "dropped", "pm-1", true))
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	require.Equal(t, 3, deliveredCount(t, store, "cancel-watch"),
		"one status.changed for the root and one for each of the two cancelled descendants")
}

// TestLedgerDispatch_CascadeCancel_PulledEventsFireOncePerDescendantOnReplica
// is the pulling side: a replica that applies the origin's events fires
// status.changed once for each cancelled descendant too.
func TestLedgerDispatch_CascadeCancel_PulledEventsFireOncePerDescendantOnReplica(t *testing.T) {
	svc, origin, _ := newTestNodeService(t)
	_, replica, _ := newTestNodeService(t)
	ctx := context.Background()
	rootID, _ := seedCancelTree(t, svc)
	applyPending(t, origin, replica)
	dir := t.TempDir()
	writeHooks(t, dir, cancelledWakeHook)

	require.NoError(t, svc.CancelNode(ctx, rootID, "dropped", "pm-1", true))
	applyPending(t, origin, replica)
	service.NewHooksDispatcher(replica, dir, slog.Default()).Dispatch(ctx)

	require.Equal(t, 3, deliveredCount(t, replica, "cancel-watch"),
		"the pulled root event and the two pulled descendant events each fire once")
}

// applyPending applies every pending event of origin to replica in one
// transaction, as a pull does, and marks them pushed.
func applyPending(t *testing.T, origin, replica *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	events, err := origin.ReadPendingEvents(ctx, 10000)
	require.NoError(t, err)
	require.NotEmpty(t, events)
	require.NoError(t, replica.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range events {
			if aErr := sqlite.IdempotentApply(ctx, tx, e); aErr != nil {
				return aErr
			}
		}
		return nil
	}))
	for _, e := range events {
		_, err := origin.WriteDB().ExecContext(ctx,
			`UPDATE sync_events SET sync_status = 'pushed' WHERE event_id = ?`, e.EventID)
		require.NoError(t, err)
	}
}
