// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// ownEvents must carry every field OwnEntities reads: a mirrored (pulled) event
// is not own because of its status, and a link's target is read from its payload.
func TestOwnEvents_CarriesStatusAndPayloadToTheOwnerCheck(t *testing.T) {
	events := []sqlite.LocalEvent{
		{NodeID: "TEST-1", Op: "link_dep", Status: "pending", Payload: `{"depends_on_node_id":"TEST-2"}`},
		{NodeID: "TEST-3", Op: "update_field", Status: "applied", Payload: `{"field":"title"}`},
	}
	got := ownEvents(events)
	require.Len(t, got, 2)
	assert.Equal(t, service.OwnEvent{NodeID: "TEST-1", Op: "link_dep", Status: "pending", Payload: events[0].Payload}, got[0])
	assert.Equal(t, service.OwnEvent{NodeID: "TEST-3", Op: "update_field", Status: "applied", Payload: events[1].Payload}, got[1])
	assert.Empty(t, ownEvents(nil))
}
