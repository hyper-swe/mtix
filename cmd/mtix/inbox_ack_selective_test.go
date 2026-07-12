// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-55: the CLI `inbox ack` is SELECTIVE by default (acks only the given
// seqs) and cumulative only with --through. This guards that routing end-to-end;
// the ack semantics themselves are covered by the store tests.
package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func seedThreeAddressed(t *testing.T, agent string) (int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		node, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "T", Creator: "w"})
		require.NoError(t, err)
		require.NoError(t, app.store.SetAnnotations(ctx, node.ID, []model.Annotation{
			{ID: "ann" + strconv.Itoa(i), Author: "w", Text: "ruling", CreatedAt: now, Addressee: agent},
		}))
	}
	got, err := app.store.InboxList(ctx, agent)
	require.NoError(t, err)
	require.Len(t, got, 3)
	return got[0].Seq, got[1].Seq, got[2].Seq
}

func inboxSeqs(t *testing.T, agent string) []int64 {
	t.Helper()
	got, err := app.store.InboxList(context.Background(), agent)
	require.NoError(t, err)
	out := make([]int64, len(got))
	for i, e := range got {
		out[i] = e.Seq
	}
	return out
}

func TestRunInboxAck_SelectiveByDefault(t *testing.T) {
	initTestApp(t)
	s1, s2, s3 := seedThreeAddressed(t, "opus")

	// Ack only the highest seq, selectively.
	require.NoError(t, runInboxAck("opus", []string{strconv.FormatInt(s3, 10)}, false))

	require.Equal(t, []int64{s1, s2}, inboxSeqs(t, "opus"),
		"selective ack of s3 must leave s1 and s2 in the inbox")
}

func TestRunInboxAck_ThroughIsCumulative(t *testing.T) {
	initTestApp(t)
	_, s2, s3 := seedThreeAddressed(t, "opus")

	require.NoError(t, runInboxAck("opus", []string{strconv.FormatInt(s2, 10)}, true))

	require.Equal(t, []int64{s3}, inboxSeqs(t, "opus"),
		"--through s2 acks s1 and s2; only s3 remains")
}
