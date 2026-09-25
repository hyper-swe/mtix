// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestIdempotentApply_HeldEventTamperedHubCopy_MergesLocalRowClocks: the
// own-event rule acknowledges a held event with the clocks of this
// replica's local sync_events row, never with the hub copy's (MTIX-95.11
// round 4), so a hub row changed after the push cannot move the local
// Lamport or vector clock; applied_events records the local row's clock.
func TestIdempotentApply_HeldEventTamperedHubCopy_MergesLocalRowClocks(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	setDescription(t, s, "MTIX-1", "emitted")
	id := latestUpdateFieldEventID(t, raw)
	local := readWireEvent(t, raw, id)
	lamportBefore, ok := metaValue(t, raw, "meta.sync.lamport")
	require.True(t, ok)
	vcBefore, ok := metaValue(t, raw, "meta.sync.vector_clock")
	require.True(t, ok)
	tampered := *local
	tampered.LamportClock = local.LamportClock + 1_000_000
	tampered.VectorClock = model.VectorClock{local.AuthorID: 5_000_000, "stranger": 7}

	pullEvents(t, s, []*model.SyncEvent{&tampered})

	lamport, _ := metaValue(t, raw, "meta.sync.lamport")
	require.Equal(t, lamportBefore, lamport, "the local Lamport clock keeps the local row's value")
	vcRaw, _ := metaValue(t, raw, "meta.sync.vector_clock")
	var vc, before model.VectorClock
	require.NoError(t, json.Unmarshal([]byte(vcRaw), &vc))
	require.NoError(t, json.Unmarshal([]byte(vcBefore), &before))
	require.Equal(t, before, vc, "the local vector clock keeps the local row's values")
	require.Equal(t, 1, countRows(t, raw,
		`SELECT COUNT(*) FROM applied_events WHERE event_id = ? AND applied_by_lamport = ?`, id, local.LamportClock),
		"applied_events records the local row's clock")
}

// TestIdempotentApply_HeldEventLocalClockUnreadable_ReturnsError: when the
// local row's vector clock cannot be decoded, the own-event rule fails with
// the event id instead of acknowledging the event with the pulled copy's
// clocks, and the batch rolls back.
func TestIdempotentApply_HeldEventLocalClockUnreadable_ReturnsError(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	setDescription(t, s, "MTIX-1", "emitted")
	id := latestUpdateFieldEventID(t, raw)
	held := readWireEvent(t, raw, id)
	_, err := raw.Exec(`UPDATE sync_events SET vector_clock = '{broken' WHERE event_id = ?`, id)
	require.NoError(t, err)

	err = applyEventsInTx(s, []*model.SyncEvent{held})

	require.ErrorContains(t, err, id)
	require.ErrorContains(t, err, "own-event check: decode local vector_clock")
	require.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, id))
}
