// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const hostileSyncID = "evt\x1b[31mred\x1b[0m\nINJECTED-\u202eg-\u00e9\u4e16"

const hostileAnnotationID = hostileSyncID + "-note"

func applyHostileEvent(t *testing.T, st *sqlite.Store, id string, op model.OpType, payload any, lamport int64) {
	t.Helper()
	pl, err := model.EncodePayload(payload)
	require.NoError(t, err)
	ev := &model.SyncEvent{
		EventID: id, ProjectPrefix: "HS", NodeID: "HS-1", OpType: op, Payload: pl,
		WallClockTS: lamport, LamportClock: lamport, VectorClock: model.VectorClock{"remote": lamport},
		AuthorID: "remote", AuthorMachineHash: "0123456789abcdef", SyncStatus: model.SyncStatusApplied,
	}
	require.NoError(t, st.WithTx(context.Background(), func(tx *sql.Tx) error {
		return sqlite.IdempotentApply(context.Background(), tx, ev)
	}))
}

func statusTextASCII(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	st, ok := status.FromError(err)
	require.True(t, ok)
	for i := 0; i < len(st.Message()); i++ {
		if st.Message()[i] < 0x20 || st.Message()[i] > 0x7e {
			t.Fatalf("raw byte 0x%02x in gRPC status text %q", st.Message()[i], st.Message())
		}
	}
}

// TestHandlers_HostileUIDAndAnnotationID_StatusTextIsSafeAndNodeExact drives
// the real handlers over a node whose uid and annotation id are pulled event
// ids: the returned node keeps the exact values, and every error status text
// is printable ASCII.
func TestHandlers_HostileUIDAndAnnotationID_StatusTextIsSafeAndNodeExact(t *testing.T) {
	s := testGRPCServer(t)
	ctx := context.Background()
	applyHostileEvent(t, s.store, hostileSyncID, model.OpCreateNode, &model.CreateNodePayload{Title: "Synced"}, 1)
	applyHostileEvent(t, s.store, hostileAnnotationID, model.OpComment,
		&model.CommentPayload{AuthorID: "remote", Body: "pulled"}, 2)

	n, err := s.HandleGetNode(ctx, "HS-1")
	require.NoError(t, err)
	assert.Equal(t, hostileSyncID, n.UID)
	require.Len(t, n.Annotations, 1)
	assert.Equal(t, hostileAnnotationID, n.Annotations[0].ID)

	_, err = s.HandleGetContext(ctx, "HS-1")
	require.NoError(t, err)

	// Unconditional error paths on the node's own routes: a second claim by
	// another agent and a lookup of a missing node must both fail, and their
	// status text must be printable ASCII. No gRPC route here names a stored
	// uid or annotation id in an error; story 2 for stored ids rests on the
	// mapError unit test.
	_, err = s.HandleClaim(ctx, "HS-1", "agent", false)
	require.NoError(t, err)
	_, err = s.HandleClaim(ctx, "HS-1", "other-agent", false)
	statusTextASCII(t, err)
	_, err = s.HandleClaim(ctx, "HS-404", "agent", false)
	statusTextASCII(t, err)
	err = s.HandleComment(ctx, "HS-1", "text", "author", "")
	require.NoError(t, err)
}
