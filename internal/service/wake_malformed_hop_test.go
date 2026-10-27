// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func pulledHopBatch(t *testing.T) (*sqlite.Store, *service.Alerts, []model.WakeKey) {
	t.Helper()
	t.Setenv("MTIX_WAKE_HOP", "0")
	prompts, nodes, source, _ := newTestPromptService(t)
	node, err := nodes.CreateNode(t.Context(), &service.CreateNodeRequest{
		Project: "TEST", Title: "mixed hop batch", Creator: "sender",
	})
	require.NoError(t, err)
	for _, body := range []string{"malformed metadata", "allowed boundary", "held boundary"} {
		require.NoError(t, prompts.AddAnnotation(t.Context(), node.ID, body, "sender", "worker"))
	}
	events, err := source.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	hops := []json.RawMessage{json.RawMessage("null"), json.RawMessage("32"), json.RawMessage("33")}
	index := 0
	for _, event := range events {
		if event.OpType != model.OpComment {
			continue
		}
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(event.Payload, &fields))
		fields["hop"] = hops[index]
		event.Payload, err = json.Marshal(fields)
		require.NoError(t, err)
		index++
	}
	require.Equal(t, 3, index)
	target, _, alerts := wakeServiceStore(t)
	require.NoError(t, target.WithTx(t.Context(), func(tx *sql.Tx) error {
		for _, event := range events {
			if err := sqlite.IdempotentApply(t.Context(), tx, event); err != nil {
				return err
			}
		}
		return nil
	}))
	entries, err := target.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, entries, 3)
	var keys []model.WakeKey
	for _, entry := range entries {
		key := wakeServiceKey()
		key.AgentID = "worker"
		key.EventSeq = entry.Seq
		keys = append(keys, key)
	}
	return target, alerts, keys
}

func TestWakeDispatcher_MalformedHopMixedWithBoundaries_NoRecurringFailure(t *testing.T) {
	testutil.OperatorState(t)
	store, alerts, keys := pulledHopBatch(t)
	var hops []int64
	dispatcher := wakeDispatcher(t, store, alerts, func(_ context.Context, attempt model.WakeAttempt) error { hops = append(hops, attempt.Hop); return nil })
	require.NoError(t, dispatcher.Pass(t.Context(), keys))
	require.Equal(t, []int64{0, 32}, hops)
	require.NoError(t, dispatcher.Pass(t.Context(), keys), "terminal events must not recur as pass errors")
	require.Equal(t, []int64{0, 32}, hops, "neither delivered event may be sent twice")
	rows, err := alerts.Recent(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "wake.hop-depth", rows[0].Class)
	require.Contains(t, rows[0].Fix, "mtix config set wake.max_hop_depth 33")
	var count int
	require.NoError(t, store.ReadDB().QueryRowContext(t.Context(), "SELECT count(*) FROM wake_ledger").Scan(&count))
	require.Equal(t, 2, count, "the depth33 event must never be claimed")
	inbox, err := store.InboxList(t.Context(), "worker")
	require.NoError(t, err)
	require.Len(t, inbox, 3, "dispatch never acknowledges an event")
}
