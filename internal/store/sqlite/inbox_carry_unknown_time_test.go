// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-112.3: a watermark ack travels without a time. Importing it must not
// stamp the import time or report it as a selective ack; an ack that did carry
// a time keeps it.
func TestImportInboxCarry_WatermarkAck_StaysTimeUnknownWithItsKind(t *testing.T) {
	ctx := context.Background()
	oldS, newS, ids := carryFixture(t)
	carry, err := oldS.ExportInboxCarry(ctx)
	require.NoError(t, err)
	_, err = newS.ImportInboxCarry(ctx, carry)
	require.NoError(t, err)
	hist, err := newS.InboxHistory(ctx, "opus")
	require.NoError(t, err)
	seqOf := map[string]int64{}
	for _, h := range hist {
		seqOf[h.EventID] = h.Seq
	}
	info, err := newS.InboxAckInfo(ctx, "opus", seqOf[ids[0]])
	require.NoError(t, err)
	require.Equal(t, sqlite.AckInfo{Kind: sqlite.AckWatermark}, info, "no invented time, no invented kind")
	info, err = newS.InboxAckInfo(ctx, "opus", seqOf[ids[2]])
	require.NoError(t, err)
	require.Equal(t, sqlite.AckSelective, info.Kind, "an ack that carried its time keeps its kind")
	require.True(t, info.TimeKnown)
}
