// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"database/sql"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"

	"github.com/stretchr/testify/require"
)

func TestRelayIngest_DuplicateDoesNotRequeue(t *testing.T) {
	r := newPeerRig(t)
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "once"})
	for i, want := range []int{1, 1, 0} {
		if i == 2 {
			_, err := r.store.WriteDB().ExecContext(t.Context(), "UPDATE sync_events SET sync_status='pushed' WHERE event_id=?", e.EventID)
			require.NoError(t, err)
		}
		r.publish(t, fleetKey, marshalEvents(t, e)...)
		require.Equal(t, 1, ingestAll(t, r).Applied)
		out, err := r.store.ReadPendingEvents(t.Context(), 2)
		require.NoError(t, err)
		require.Len(t, out, want)
		require.Equal(t, 1, r.journalCount(t))
	}
}

func TestRelayIngest_DuplicateDoesNotRequeueAfterHubPull(t *testing.T) {
	r := newPeerRig(t)
	e := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "forward me"})
	r.publish(t, fleetKey, marshalEvents(t, e)...)
	require.Equal(t, 1, ingestAll(t, r).Applied)
	out, err := r.store.ReadPendingEvents(t.Context(), 2)
	require.NoError(t, err)
	require.Len(t, out, 1)

	// Another gateway reached the hub first. A different envelope under
	// this id must still go through push's mismatch handling.
	hub := e
	hub.Payload = []byte(`{"title":"other content"}`)
	apply := func() error {
		return r.store.WithTx(t.Context(), func(tx *sql.Tx) error {
			return r.store.IdempotentApply(t.Context(), tx, &hub)
		})
	}
	require.NoError(t, apply())
	out, err = r.store.ReadPendingEvents(t.Context(), 2)
	require.NoError(t, err)
	require.Len(t, out, 1)
	hub.Payload = e.Payload
	require.NoError(t, apply())
	r.publish(t, fleetKey, marshalEvents(t, e)...)
	require.Equal(t, 1, ingestAll(t, r).Applied)
	out, err = r.store.ReadPendingEvents(t.Context(), 2)
	require.NoError(t, err)
	require.Empty(t, out)
	require.Equal(t, 1, r.journalCount(t))
}
