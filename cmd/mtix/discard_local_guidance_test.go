// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// requireDiscardLocalGuard asserts text states the guard on
// `mtix sync reconcile --discard-local` (MTIX-95.11.3): what it deletes,
// push first, pending 0, the typed ticket count at an interactive terminal,
// and that it cannot run unattended; and that it uses neither "human" nor
// "go-ahead".
func requireDiscardLocalGuard(t *testing.T, name, text string) {
	t.Helper()
	flat := strings.Join(strings.Fields(text), " ")
	for _, want := range []string{
		"deletes local tasks and unpushed changes",
		"mtix sync push",
		"pending 0",
		"ticket count",
		"typed at an interactive terminal",
		"cannot run unattended",
	} {
		assert.Contains(t, flat, want, name)
	}
	lower := strings.ToLower(flat)
	assert.NotContains(t, lower, "human", name)
	assert.NotContains(t, lower, "go-ahead", name)
}

// TestDiscardLocalGuidance_EverySuggestion_StatesTheGuard pins the guard in
// each CLI text that suggests --discard-local.
func TestDiscardLocalGuidance_EverySuggestion_StatesTheGuard(t *testing.T) {
	requireDiscardLocalGuard(t, "cloneRecovery", cloneRecovery)
	requireDiscardLocalGuard(t, "clone refusal on non-empty local events", errCloneLocalNotEmpty().Error())
	assert.Contains(t, errCloneLocalNotEmpty().Error(), "--resume")
	requireDiscardLocalGuard(t, "clone Long help", newSyncCloneCmd().Long)
	requireDiscardLocalGuard(t, "reconcile Long help", newSyncReconcileCmd().Long)
	requireDiscardLocalGuard(t, "divergent history guide", sqlite.DivergentHistoryGuide)
}

// TestPrintReconcilePlan_DiscardLocal_StatesTheGuardAndNodeCountOnly pins the
// dry-run output of --discard-local.
func TestPrintReconcilePlan_DiscardLocal_StatesTheGuardAndNodeCountOnly(t *testing.T) {
	var buf bytes.Buffer
	printReconcilePlan(&buf, sqlite.Plan{Path: "discard-local", NodeCount: 7}, true)
	out := buf.String()
	assert.Contains(t, out, "DRY RUN")
	assert.Contains(t, out, "node_count: 7")
	assert.Contains(t, out, "shows only the node count")
	requireDiscardLocalGuard(t, "dry-run output", out)

	buf.Reset()
	printReconcilePlan(&buf, sqlite.Plan{Path: "rename-to", NewPrefix: "NEW", NodeCount: 2}, true)
	assert.NotContains(t, buf.String(), "ticket count", "the guard belongs to --discard-local only")
}

// TestReadCloneCheckpoint_Negative_PrefersNonDestructivePathAndStatesTheGuard
// pins the negative-checkpoint error: restore a backup or pull on a fresh
// store come first, and --discard-local carries the guard.
func TestReadCloneCheckpoint_Negative_PrefersNonDestructivePathAndStatesTheGuard(t *testing.T) {
	initTestApp(t)
	setPeerMeta(t, "meta.sync.clone.checkpoint", "-5")

	_, err := readCloneCheckpoint(context.Background(), app.store, true)

	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "negative")
	assert.Less(t, strings.Index(msg, "restore a backup"), strings.Index(msg, "--discard-local"))
	assert.Contains(t, msg, "mtix sync pull")
	requireDiscardLocalGuard(t, "negative checkpoint", msg)
}

// TestFormatBackfillError_NonEmptyJournal_StaysADoNotUse pins that the
// backfill advice warns against --discard-local rather than suggesting it.
func TestFormatBackfillError_NonEmptyJournal_StaysADoNotUse(t *testing.T) {
	err := formatBackfillError(&bytes.Buffer{}, sqlite.ErrBackfillSyncEventsNonEmpty)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DELETES EVERY TICKET")
	assert.Contains(t, err.Error(), "do not use it")
	assert.False(t, errors.Is(err, nil))
}
