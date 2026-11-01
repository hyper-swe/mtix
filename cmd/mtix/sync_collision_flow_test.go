// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// fakeCollisionHub records the calls the resolve flow makes to the hub.
type fakeCollisionHub struct {
	collision               transport.OpenCollision
	decision                string // winner recorded on the hub row
	path                    string // loser_new_path recorded on the hub row
	resolveOK               bool   // whether the guarded update lands
	resolveErr, completeErr error
	resolves, completes     int
}

func (h *fakeCollisionHub) GetCollision(context.Context, int64) (transport.OpenCollision, string, string, error) {
	return h.collision, h.decision, h.path, nil
}

func (h *fakeCollisionHub) ResolveCollision(_ context.Context, _ int64, winner, _, _ string) (bool, error) {
	h.resolves++
	if h.resolveErr != nil {
		return false, h.resolveErr
	}
	if h.resolveOK {
		h.decision = winner
	}
	return h.resolveOK, nil
}

func (h *fakeCollisionHub) CompleteCollisionMove(context.Context, int64, string, string) error {
	h.completes++
	return h.completeErr
}

func testCollision() transport.OpenCollision {
	return transport.OpenCollision{CollisionID: 42, DisplayPath: "TEST-1.1",
		HeldEventID: "held-ev", HeldUID: "held-uid", IncomingEventID: "incoming-ev", IncomingUID: "incoming-uid"}
}

// A decision already recorded for the other side is refused before the hub or
// the local store is touched: the pre-check in resolveConnectedCollision.
func TestResolveConnectedCollision_OtherWinnerAlreadyRecorded_RefusesUntouched(t *testing.T) {
	initTestApp(t)
	hub := &fakeCollisionHub{collision: testCollision(), decision: "held-ev"}
	err := resolveConnectedCollision(context.Background(), &bytes.Buffer{}, hub, 42, "incoming")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "different winner")
	assert.Zero(t, hub.resolves, "the hub is not asked to accept")
	assert.Zero(t, hub.completes)
}

// The guarded hub update can lose a race with another operator; acceptCollision
// then verifies that the recorded decision is the one asked for.
func TestAcceptCollision_LostGuardedUpdate_AcceptsOnlyTheSameWinner(t *testing.T) {
	tests := []struct {
		name    string
		hub     *fakeCollisionHub
		wantErr string
	}{
		{"lost the race but the same winner is recorded", &fakeCollisionHub{decision: "incoming-ev"}, ""},
		{"lost the race to the other winner", &fakeCollisionHub{decision: "held-ev"}, "different winner"},
		{"the hub update fails", &fakeCollisionHub{resolveErr: errors.New("permission denied")}, "permission denied"},
		{"the update lands", &fakeCollisionHub{resolveOK: true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := acceptCollision(context.Background(), tt.hub, 42, "incoming-ev")
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// A run on a client that does not hold the losing task must leave the hub row
// open (the base behaviour): the local check comes before the hub accept.
func TestResolveConnectedCollision_LoserNotInThisStore_LeavesTheHubRowOpen(t *testing.T) {
	initTestApp(t)
	hub := &fakeCollisionHub{collision: testCollision(), resolveOK: true}
	err := resolveConnectedCollision(context.Background(), &bytes.Buffer{}, hub, 42, "held")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in this local store")
	assert.Zero(t, hub.resolves, "the hub row stays open")
	assert.Empty(t, hub.decision)
}

// Accepted collisions whose local move is pending are listed with the command
// that finishes them; open ones keep their old shape.
func TestWriteCollisionList_PendingMoves_ShownWithTheResumeCommand(t *testing.T) {
	open := []transport.OpenCollision{testCollision()}
	pendingColl := testCollision()
	pendingColl.CollisionID = 7
	pending := []transport.PendingCollisionMove{{OpenCollision: pendingColl, WinnerEventID: "incoming-ev"}}

	var text bytes.Buffer
	require.NoError(t, writeCollisionList(&text, open, pending, false))
	assert.Contains(t, text.String(), "1 accepted collision(s) waiting for the local move")
	assert.Contains(t, text.String(), "mtix sync collisions resolve 7 --winner incoming")

	var raw bytes.Buffer
	require.NoError(t, writeCollisionList(&raw, open, pending, true))
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(raw.Bytes(), &rows))
	require.Len(t, rows, 2)
	assert.NotContains(t, rows[0], "pending_local_move", "an open collision keeps its old fields")
	assert.Equal(t, true, rows[1]["pending_local_move"])
	assert.Equal(t, "incoming", rows[1]["winner"])
	assert.EqualValues(t, 7, rows[1]["collision_id"])

	var none bytes.Buffer
	require.NoError(t, writeCollisionList(&none, nil, nil, false))
	assert.NotContains(t, none.String(), "waiting for the local move")
}
