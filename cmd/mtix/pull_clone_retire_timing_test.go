// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func unfinishedCloneForPullTiming(t *testing.T) string {
	t.Helper()
	initTestApp(t)
	events := remoteCreates(t, 1, 2)
	require.NoError(t, applyBatch(t.Context(), testIngest(nil), app.store, events))
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(2), tail)
	witness := cloneCompletionWitness(t)
	require.NotEmpty(t, witness)
	return witness
}

func requirePullTimingJournalUnchanged(t *testing.T) {
	t.Helper()
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	require.Equal(t, int64(2), tail)
	cursor, err := readLastPulledClock(t.Context(), app.store)
	require.NoError(t, err)
	require.Zero(t, cursor.Lamport)
}

func TestPullCompletion_FailedContact_PreservesCloneWitness(t *testing.T) {
	witness := unfinishedCloneForPullTiming(t)
	t.Setenv(transport.EnvDSN, "postgres://127.0.0.1:1/pull-retirement?sslmode=disable")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	require.ErrorContains(t, runSyncPull(ctx, &stdout, &stderr, nil, cloudOpts, 1), "connect")
	requirePullTimingJournalUnchanged(t)
	assert.Equal(t, witness, cloneCompletionWitness(t), "failed contact is not completion")
}

func TestPullCompletion_FailedFirstFetch_PreservesCloneWitness(t *testing.T) {
	witness := unfinishedCloneForPullTiming(t)
	fault := errors.New("forced first cursor fetch failure")
	hub := &fakeLateHub{pullErr: fault}
	pulled, batches, err := pullLoop(t.Context(), testIngest(&bytes.Buffer{}), hub, app.store, transport.PullCursor{}, 1)
	require.ErrorIs(t, err, fault)
	require.Zero(t, pulled)
	require.Zero(t, batches)
	require.Len(t, hub.pullCursors, 1, "the first fetch really failed with a live context")
	requirePullTimingJournalUnchanged(t)
	assert.Equal(t, witness, cloneCompletionWitness(t), "a failed fetch never retires the stream")
}

func TestPullCompletion_AllRefusedBatches_RetireOnlyAfterDrain(t *testing.T) {
	_ = unfinishedCloneForPullTiming(t)
	events := []*model.SyncEvent{remoteCreateAt(t, "TEST-3", 1<<53), remoteCreateAt(t, "TEST-4", (1<<53)+1)}
	hub := &fakeLateHub{pullEvents: events}
	pulled, batches, err := pullLoop(t.Context(), testIngest(&bytes.Buffer{}), hub, app.store, transport.PullCursor{}, 1)
	require.NoError(t, err)
	require.Zero(t, pulled)
	require.Equal(t, 2, batches, "both real quarantine transactions committed")
	require.Len(t, quarantined(t), 2, "all events were refused, not applied or skipped")
	requirePullTimingJournalUnchanged(t)
	assert.Empty(t, cloneCompletionWitness(t), "a drained pass retires even without an accepted cursor")
}
