// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// Real clone, pull, retry and own-event routes must use the store clock.
func clockApplyTime() time.Time {
	return time.Date(2001, 2, 3, 4, 5, 6, 123456789, time.FixedZone("test", 3600))
}

func requireAppliedClock(t *testing.T, e *model.SyncEvent) {
	t.Helper()
	var applied, mirrored string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT applied_at FROM applied_events WHERE event_id = ?`, e.EventID).Scan(&applied))
	require.Equal(t, clockApplyTime().UTC().Format(time.RFC3339Nano), applied)
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT created_at FROM sync_events WHERE event_id = ?`, e.EventID).Scan(&mirrored))
	require.Equal(t, clockApplyTime().UTC().Format(time.RFC3339Nano), mirrored)
}

func requireNodeApplyClock(t *testing.T, id string) {
	t.Helper()
	var updated string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT updated_at FROM nodes WHERE id = ?`, id).Scan(&updated))
	require.Equal(t, clockApplyTime().UTC().Format(time.RFC3339), updated)
}

func TestSyncApply_StoreClock_ReachesCloneAndPull(t *testing.T) {
	for _, route := range []string{"clone", "pull"} {
		t.Run(route, func(t *testing.T) {
			initTestApp(t)
			app.store.SetClock(clockApplyTime)
			events := offlineEvents(t)
			if route == "clone" {
				require.NoError(t, applyBatch(context.Background(), testIngest(nil), app.store, events))
			} else {
				held, err := applyPullBatch(context.Background(), testIngest(nil), app.store, quarantineSourcePull, events)
				require.NoError(t, err)
				require.Empty(t, held)
			}
			for _, e := range events {
				requireAppliedClock(t, e)
			}
			requireNodeApplyClock(t, "TEST-9")
		})
	}
}

func TestSyncApply_StoreClock_ReachesQuarantineRetry(t *testing.T) {
	initTestApp(t)
	app.store.SetClock(clockApplyTime)
	events := offlineEvents(t)
	strict := testIngest(nil)
	strict.maxJump = 1
	events[0].LamportClock = 5
	held, err := applyPullBatch(context.Background(), strict, app.store, quarantineSourcePull, events[:1])
	require.NoError(t, err)
	require.Len(t, held, 1)
	result, err := retryQuarantinedEvents(context.Background(), testIngest(nil), app.store, 100)
	require.NoError(t, err)
	require.Equal(t, 1, result.applied)
	requireAppliedClock(t, events[0])
	requireNodeApplyClock(t, "TEST-9")
}

func TestSyncApply_StoreClock_ReachesOwnAcknowledgement(t *testing.T) {
	initTestApp(t)
	require.NoError(t, app.store.CreateNode(context.Background(), mkPGNode("TEST-1", "", 0, 1, "own")))
	events, err := readPendingBatch(context.Background(), app.store, 100)
	require.NoError(t, err)
	require.Len(t, events, 1)
	app.store.SetClock(clockApplyTime)
	held, err := applyPullBatch(context.Background(), testIngest(nil), app.store, quarantineSourcePull, events)
	require.NoError(t, err)
	require.Empty(t, held)
	var applied string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT applied_at FROM applied_events WHERE event_id = ?`, events[0].EventID).Scan(&applied))
	require.Equal(t, clockApplyTime().UTC().Format(time.RFC3339Nano), applied)
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(), `SELECT COUNT(*) FROM sync_events`).Scan(&n))
	require.Equal(t, 1, n, "acknowledgement does not dispatch or mirror")
}

func TestSyncApply_StoreClock_ReachesDerivedDependencyChanges(t *testing.T) {
	for _, op := range []model.OpType{model.OpUnlinkDep, model.OpTransitionStatus} {
		t.Run(string(op), func(t *testing.T) {
			initTestApp(t)
			from, target, link := linkDepEvents(t)
			require.NoError(t, applyBatch(context.Background(), testIngest(nil), app.store, []*model.SyncEvent{from, target}))
			app.store.SetClock(clockApplyTime)
			require.NoError(t, applyBatch(context.Background(), testIngest(nil), app.store, []*model.SyncEvent{link}))
			requireNodeApplyClock(t, "TEST-2")
			next := *link
			next.EventID = "0193fa00-0000-7000-8000-000000107701"
			next.LamportClock++
			next.OpType = op
			var payload any = &model.UnlinkDepPayload{DependsOnNodeID: "TEST-2", DepType: "blocks"}
			if op == model.OpTransitionStatus {
				payload = &model.TransitionStatusPayload{From: model.StatusOpen, To: model.StatusDone}
			}
			var err error
			next.Payload, err = model.EncodePayload(payload)
			require.NoError(t, err)
			require.NoError(t, applyBatch(context.Background(), testIngest(nil), app.store, []*model.SyncEvent{&next}))
			requireNodeApplyClock(t, "TEST-2")
		})
	}
}
