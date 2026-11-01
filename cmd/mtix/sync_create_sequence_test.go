// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// MTIX-108: all inbound paths must advance counters, even when allocation's
// collision recovery could otherwise hide a stale counter.
func TestSyncCreateSequence_NextLocalCreate(t *testing.T) {
	for _, path := range []string{"pull", "clone", "sweep"} {
		t.Run(path, func(t *testing.T) {
			initTestApp(t)
			setPeerMeta(t, "meta.sync.machine_hash", "0123456789abcdef")
			events := []*model.SyncEvent{remoteCreateAt(t, "TEST-1", 1), remoteCreateAt(t, "TEST-1.1", 2)}
			payload, err := model.EncodePayload(model.CreateNodePayload{
				Title: "teammate child", ParentID: "TEST-1", Priority: model.PriorityMedium,
			})
			require.NoError(t, err)
			events[1].Payload = payload
			applySequencePath(t, path, events)
			for _, parent := range []string{"", "TEST-1"} {
				var counter int
				require.NoError(t, app.store.QueryRow(context.Background(),
					`SELECT value FROM sequences WHERE key = ?`, "TEST:"+parent).Scan(&counter))
				require.Equal(t, 1, counter, "applied create advances parent counter")
				node, err := app.nodeSvc.CreateNode(context.Background(), &service.CreateNodeRequest{
					Project: "TEST", ParentID: parent, Title: "local after " + path, Creator: "local",
				})
				require.NoError(t, err)
				require.Equal(t, model.BuildID("TEST", parent, 2), node.ID)
			}
		})
	}
}

func applySequencePath(t *testing.T, path string, events []*model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	hub := &fakeLateHub{pullEvents: events, events: events, hubNows: []time.Time{events[1].CreatedAt}}
	switch path {
	case "pull":
		n, _, err := pullLoop(ctx, testIngest(nil), hub, app.store, transport.PullCursor{}, 1)
		require.NoError(t, err)
		require.Equal(t, 2, n)
	case "clone":
		n, _, stage, err := checkThenClone(ctx, &bytes.Buffer{}, hub, transport.PullCursor{}, 1)
		require.NoError(t, err, stage)
		require.Equal(t, 2, n)
	case "sweep":
		hub.pullEvents = nil
		n, _, err := pullLoop(ctx, testIngest(nil), hub, app.store, transport.PullCursor{Lamport: 10}, 1)
		require.NoError(t, err)
		require.Zero(t, n, "cursor pass misses late creates")
		result, err := sweepLateEvents(ctx, testIngest(nil), hub, app.store, 1)
		require.NoError(t, err)
		require.True(t, result.Completed)
		require.Equal(t, 2, result.Recovered)
	}
}
