// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// A comment never overtakes its node's create: the push the send requests is
// the whole pending prefix in Lamport order (ADR-007 D21, MTIX-112.20.2).
func TestAddressedSend_PrefixOrder_CreateBeforeComment(t *testing.T) {
	initTestApp(t)
	id := seedOneNode(t)
	other := seedOneNode(t) // an unrelated earlier event is part of the prefix too
	require.NoError(t, runComment(id, "please review", "agent-b"))

	hub := newFakePushHub()
	var stderr bytes.Buffer
	_, err := pushLoop(context.Background(), &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())

	sent := hub.events
	createIdx, otherIdx, commentIdx := -1, -1, -1
	for i, e := range sent {
		switch {
		case e.OpType == model.OpCreateNode && e.NodeID == id:
			createIdx = i
		case e.OpType == model.OpCreateNode && e.NodeID == other:
			otherIdx = i
		case e.OpType == model.OpComment && e.NodeID == id:
			commentIdx = i
		}
	}
	require.GreaterOrEqual(t, createIdx, 0, "the node's create is in the push")
	require.GreaterOrEqual(t, otherIdx, 0, "the whole pending prefix is pushed, not only the comment")
	require.GreaterOrEqual(t, commentIdx, 0, "the addressed comment is in the push")
	require.Less(t, createIdx, commentIdx, "a comment must never reach the hub before its node's create")
	for i := 1; i < len(sent); i++ {
		require.LessOrEqual(t, sent[i-1].LamportClock, sent[i].LamportClock, "Lamport order")
	}
}
