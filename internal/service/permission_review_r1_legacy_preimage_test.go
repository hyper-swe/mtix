// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Synthesize the accepted pre-preimage format, not an old binary/API execution.
// All pending/canonical confirmation hashes are made coherent before assertions.
func reviewR1LegacyPending(t *testing.T, f *wakePermissionFixture) oplocal.PermissionBatch {
	t.Helper()
	var pending map[string]json.RawMessage
	reports, err := f.state.ReadJSON("permissions-pending", &pending)
	require.NoError(t, err)
	require.Empty(t, reports)
	var batch oplocal.PermissionBatch
	require.NoError(t, json.Unmarshal(pending["batch"], &batch))
	rows, err := f.state.Confirmations(t.Context())
	require.NoError(t, err)
	batch.PublicationID = ""
	raw, err := json.Marshal(batch)
	require.NoError(t, err)
	hash := sha256.Sum256(raw)
	pending["batch"] = raw
	delete(pending, "before_agent_grants")
	delete(pending, "before_hash")
	var encoded bytes.Buffer
	for _, row := range rows {
		if row.ID == batch.ID {
			row.BatchHash = hex.EncodeToString(hash[:])
		}
		require.NoError(t, json.NewEncoder(&encoded).Encode(row))
	}
	require.NoError(t, f.state.WriteJSON("permissions-pending", pending))
	reviewR1LegacyReplay(t, f, batch.ID, raw, hex.EncodeToString(hash[:]))
	dir, err := f.state.Path()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "confirmations.jsonl"), encoded.Bytes(), 0600))
	_, err = f.state.Confirmations(t.Context())
	require.NoError(t, err, "legacy fixture must be internally coherent before behavior assertions")
	return batch
}

func reviewR1InterruptedLegacyAllow(t *testing.T, hub bool) *wakePermissionFixture {
	t.Helper()
	f := newWakePermissionFixture(t)
	permissionRoot(t, f, "TEST-1")
	f.register(t, "first")
	if hub {
		_, err := f.store.MarkHubConnected(t.Context(), "config")
		require.NoError(t, err)
	}
	require.NoError(t, reviewR1Apply(t, f, "first", "deny"))
	f.svc.Store = &reviewR1NoticeStore{PermissionStore: f.store}
	require.Error(t, reviewR1Apply(t, f, "first", "allow"))
	reviewR1LegacyPending(t, f)
	return f
}

func TestReviewR1_LegacyPendingAllowCannotFallThroughLocalImplicitEligibility(t *testing.T) {
	for _, hub := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "hub"}[hub], func(t *testing.T) {
			f := reviewR1InterruptedLegacyAllow(t, hub)
			ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
			require.NoError(t, err)
			assert.False(t, ok, "legacy unfinished allow cannot bypass the earlier operator deny")
			assert.NotEqual(t, "implicit local registration", reason)
		})
	}
}

func TestReviewR1_LegacyQuarantineCannotReenableAnEarlierDeniedLocalID(t *testing.T) {
	f := reviewR1InterruptedLegacyAllow(t, false)
	f.register(t, "second")
	// Other-agent denial must complete while the older publication stays unavailable.
	require.NoError(t, reviewR1Apply(t, f, "second", "deny"))
	reviewR1Denied(t, f, "second")
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	assert.False(t, ok, "quarantine must not turn ambiguous legacy state into implicit eligibility")
	assert.NotEqual(t, "implicit local registration", reason)
}

func TestReviewR1_LegacyUnrelatedAllowCannotFinishAndClearOldHold(t *testing.T) {
	f := reviewR1InterruptedLegacyAllow(t, false)
	f.svc.Store = f.store
	require.NoError(t, reviewR1Apply(t, f, "other", "allow"))
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	assert.False(t, ok, "recovering older work for another id must retain the quarantine hold")
	assert.Equal(t, "quarantined", reason)
}

func TestReviewR1_LegacyOriginalCallerRetryCannotClearHold(t *testing.T) {
	f := reviewR1InterruptedLegacyAllow(t, false)
	pending, err := f.state.PendingPermissionBatches(t.Context())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	published := 0
	publish := func(context.Context, oplocal.PermissionBatch) error { published++; return nil }
	require.ErrorContains(t, f.state.ApplyPermissions(t.Context(), pending[0], publish), "cancelled or quarantined")
	assert.Zero(t, published, "obsolete uncommitted allow must not publish on retry")
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, "quarantined", reason)
}

func reviewR1LegacyReplay(t *testing.T, f *wakePermissionFixture, id string, batch json.RawMessage, hash string) {
	t.Helper()
	var ledger []map[string]json.RawMessage
	reports, err := f.state.ReadJSON("permissions-replays", &ledger)
	require.NoError(t, err)
	require.Empty(t, reports)
	changed := false
	for _, row := range ledger {
		var stored oplocal.PermissionBatch
		require.NoError(t, json.Unmarshal(row["batch"], &stored))
		if stored.ID == id {
			row["batch"] = batch
			row["batch_hash"], err = json.Marshal(hash)
			require.NoError(t, err)
			changed = true
		}
	}
	if changed {
		require.NoError(t, f.state.WriteJSON("permissions-replays", ledger))
	}
}

func TestReviewR1_LegacyHoldSurvivesCancelAndFileUntilFreshOperatorAllow(t *testing.T) {
	f := reviewR1InterruptedLegacyAllow(t, false)
	pending, err := f.state.PendingPermissionBatches(t.Context())
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, f.state.CancelPermissionBatch(t.Context(), pending[0].ID))
	batch := filePermissionReplayBatch()
	batch.Mode = "yes"
	require.NoError(t, f.svc.ApplyFileBatch(t.Context(), f.board, batch))
	result, available, err := f.state.PermissionReplayResult(t.Context(), f.board.ID, batch.ID)
	require.NoError(t, err)
	require.True(t, available)
	require.Equal(t, "quarantined_requires_operator", result.Changes[0].Outcome)
	holds, err := f.state.QuarantinedPermissionGrants(t.Context())
	require.NoError(t, err)
	require.Len(t, holds, 1)
	require.False(t, holds[0].Denied)
	require.Equal(t, pending[0].ID, holds[0].QuarantineBatchID)
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, "quarantined", reason)
	require.NoError(t, reviewR1Apply(t, f, "first", "allow"))
	holds, err = f.state.QuarantinedPermissionGrants(t.Context())
	require.NoError(t, err)
	require.Empty(t, holds)
	ok, _, err = f.svc.Wakeable(t.Context(), f.board, "first")
	require.NoError(t, err)
	require.True(t, ok)
}

func TestReviewR1_RegistrationCannotActivateLegacyQuarantine(t *testing.T) {
	f := newWakePermissionFixture(t)
	permissionRoot(t, f, "TEST-1")
	f.svc.Store = &reviewR1NoticeStore{PermissionStore: f.store}
	require.Error(t, reviewR1Apply(t, f, "future", "allow"))
	reviewR1LegacyPending(t, f)
	pending, err := f.svc.PendingRegistrations(t.Context(), f.board)
	require.NoError(t, err)
	require.Empty(t, pending, "a quarantine hold is not an active preconfirmation")
	f.register(t, "future")
	ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "future")
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, "quarantined", reason)
}

func TestReviewR1_LegacyQuarantinePreservesUntouchedPriorGrantsAndDenies(t *testing.T) {
	f := newWakePermissionFixture(t)
	permissionRoot(t, f, "TEST-1")
	for _, agent := range []string{"first", "kept-allow", "kept-deny"} {
		f.register(t, agent)
	}
	require.NoError(t, reviewR1Apply(t, f, "first", "deny"))
	require.NoError(t, reviewR1Apply(t, f, "kept-allow", "allow"))
	require.NoError(t, reviewR1Apply(t, f, "kept-deny", "deny"))
	before, err := f.state.AgentGrants()
	require.NoError(t, err)
	f.svc.Store = &reviewR1NoticeStore{PermissionStore: f.store}
	require.Error(t, reviewR1Apply(t, f, "first", "allow"))
	reviewR1LegacyPending(t, f)
	require.NoError(t, reviewR1Apply(t, f, "other", "deny"))
	after, err := f.state.AgentGrants()
	require.NoError(t, err)
	byAgent := map[string]oplocal.AgentGrant{}
	for _, grant := range after {
		byAgent[grant.AgentID] = grant
	}
	for _, grant := range before {
		if grant.AgentID != "first" {
			assert.Equal(t, grant, byAgent[grant.AgentID])
		}
	}
	assert.True(t, byAgent["first"].Quarantined)
}

func TestReviewR1_LegacyDenyRecoversOriginalPublicationIdentity(t *testing.T) {
	f := newWakePermissionFixture(t)
	permissionRoot(t, f, "TEST-1")
	f.svc.Store = &permissionFaultStore{PermissionStore: f.store, fail: true}
	batch := filePermissionReplayBatch()
	batch.Changes[0].Action = "deny"
	require.Error(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	legacy := reviewR1LegacyPending(t, f)
	f.svc.Store = f.store
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	require.NoError(t, f.svc.ApplyBatch(t.Context(), f.board, batch))
	node, err := f.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	require.Len(t, node.Annotations, 1)
	assert.Equal(t, legacy.ID, node.Annotations[0].ID)
	assert.Equal(t, 1, permissionWireEventCount(t, f.store, legacy.ID))
	holds, err := f.state.QuarantinedPermissionGrants(t.Context())
	require.NoError(t, err)
	require.Empty(t, holds)
	reviewR1Denied(t, f, "first")
}
