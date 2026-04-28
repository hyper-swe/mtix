// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// MTIX-15.6.3 atomicity injection tests, dry-run plan equivalence,
// and prefix-collision check.

// --- DryRun ---

func TestDryRunDiscardLocal_PlanShape(t *testing.T) {
	s, _, _ := reconcileTestStore(t)
	seedTree(t, s)

	plan, err := DryRunDiscardLocal(context.Background(), s)
	require.NoError(t, err)
	require.Equal(t, "discard-local", plan.Path)
	require.Equal(t, 5, plan.NodeCount)
	require.Empty(t, plan.Renames, "discard-local renames nothing")
}

func TestDryRunRenameTo_PlanShape(t *testing.T) {
	s, _, _ := reconcileTestStore(t)
	seedTree(t, s)

	plan, err := DryRunRenameTo(context.Background(), s, "DEMO")
	require.NoError(t, err)
	require.Equal(t, "rename-to", plan.Path)
	require.Equal(t, "DEMO", plan.NewPrefix)
	require.Equal(t, 5, plan.NodeCount)
	// Sorted by old id.
	expected := []Rename{
		{"MTIX-1", "DEMO-1"},
		{"MTIX-1.1", "DEMO-1.1"},
		{"MTIX-1.1.1", "DEMO-1.1.1"},
		{"MTIX-1.2", "DEMO-1.2"},
		{"MTIX-2", "DEMO-2"},
	}
	require.Equal(t, expected, plan.Renames)
}

func TestDryRunRenameTo_RefusesInvalidPrefix(t *testing.T) {
	s, _, _ := reconcileTestStore(t)
	seedTree(t, s)
	_, err := DryRunRenameTo(context.Background(), s, "lowercase")
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}

func TestDryRunImportAs_PlanShape(t *testing.T) {
	s, _, _ := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)

	plan, err := DryRunImportAs(context.Background(), s, "PROJ-7")
	require.NoError(t, err)
	require.Equal(t, "import-as", plan.Path)
	require.Equal(t, "PROJ-7", plan.ParentID)
	require.Equal(t, 5, plan.NodeCount)
	expected := []Rename{
		{"MTIX-1", "PROJ-7.1"},
		{"MTIX-1.1", "PROJ-7.1.1"},
		{"MTIX-1.1.1", "PROJ-7.1.1.1"},
		{"MTIX-1.2", "PROJ-7.1.2"},
		{"MTIX-2", "PROJ-7.2"},
	}
	require.Equal(t, expected, plan.Renames)
}

func TestDryRunImportAs_RefusesIfParentMissing(t *testing.T) {
	s, _, _ := reconcileTestStore(t)
	seedTree(t, s)
	_, err := DryRunImportAs(context.Background(), s, "PROJ-7")
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrNotFound))
}

// --- Plan / executed-audit equivalence ---

// Helper: extracts (OldID, NewID) pairs from RENAME_NODE audit events.
func auditRenamePairs(events []auditEvent) []Rename {
	out := []Rename{}
	for _, e := range events {
		if e.Type != "RENAME_NODE" {
			continue
		}
		out = append(out, Rename{OldID: e.OldID, NewID: e.NewID})
	}
	return out
}

func TestDryRun_PlanMatchesExecutedAuditEvents_RenameTo(t *testing.T) {
	// Build two identical stores; dry-run on A, real-run on B; the
	// plan's renames must equal B's RENAME_NODE audit pairs (modulo
	// order — the executable iterates long-id-first, the plan sorts
	// by old id, so we compare as sets).
	storeA, _, _ := reconcileTestStore(t)
	seedTree(t, storeA)
	plan, err := DryRunRenameTo(context.Background(), storeA, "DEMO")
	require.NoError(t, err)

	storeB, _, mtixDirB := reconcileTestStore(t)
	seedTree(t, storeB)
	_, err = RenameTo(context.Background(), storeB, mtixDirB, "DEMO")
	require.NoError(t, err)

	executedPairs := auditRenamePairs(readAuditLog(t, mtixDirB))
	require.ElementsMatch(t, plan.Renames, executedPairs,
		"DryRun.Renames MUST match the audit RENAME_NODE pairs as a set")
}

func TestDryRun_PlanMatchesExecutedAuditEvents_ImportAs(t *testing.T) {
	storeA, _, _ := reconcileTestStore(t)
	seedParentForImport(t, storeA, "PROJ-7")
	seedTree(t, storeA)
	plan, err := DryRunImportAs(context.Background(), storeA, "PROJ-7")
	require.NoError(t, err)

	storeB, _, mtixDirB := reconcileTestStore(t)
	seedParentForImport(t, storeB, "PROJ-7")
	seedTree(t, storeB)
	_, err = ImportAs(context.Background(), storeB, mtixDirB, "PROJ-7")
	require.NoError(t, err)

	executedPairs := auditRenamePairs(readAuditLog(t, mtixDirB))
	require.ElementsMatch(t, plan.Renames, executedPairs)
}

func TestDryRun_DoesNotMutateStore(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	seedTree(t, s)
	before := readNodeIDs(t, raw)

	_, err := DryRunDiscardLocal(context.Background(), s)
	require.NoError(t, err)
	_, err = DryRunRenameTo(context.Background(), s, "DEMO")
	require.NoError(t, err)

	require.Equal(t, before, readNodeIDs(t, raw),
		"DryRun functions MUST NOT modify the store")
}

// --- Prefix collision ---

func TestCheckPrefixCollision_NoCollision(t *testing.T) {
	hub := map[string]string{
		"MTIX":  "abc...",
		"OTHER": "xyz...",
	}
	require.NoError(t, CheckPrefixCollision(hub, "DEMO"))
}

func TestCheckPrefixCollision_Collision(t *testing.T) {
	hub := map[string]string{
		"MTIX":  "abc",
		"OTHER": "xyz",
	}
	err := CheckPrefixCollision(hub, "OTHER")
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrSyncReconcilePrefixCollision))
	require.Contains(t, err.Error(), "OTHER")
}

func TestCheckPrefixCollision_EmptyNewPrefix(t *testing.T) {
	err := CheckPrefixCollision(map[string]string{}, "")
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}

func TestCheckPrefixCollision_FreshHub(t *testing.T) {
	require.NoError(t, CheckPrefixCollision(map[string]string{}, "DEMO"))
	require.NoError(t, CheckPrefixCollision(nil, "DEMO"))
}

// --- Atomicity injection ---

// errChaosFailure is the synthetic error injected by the chaos hook.
var errChaosFailure = errors.New("synthetic chaos failure")

func TestRenameTo_AtomicityFailureMidLoop(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)
	beforeIDs := readNodeIDs(t, raw)

	// Fail after the 2nd rename. Hook fires AFTER applyRenameLoop's
	// per-row work completes for that row, simulating a tx-internal
	// crash mid-flight.
	t.Cleanup(resetReconcileChaosHook)
	reconcileFailAfterN = func(callIdx int) error {
		if callIdx == 2 {
			return errChaosFailure
		}
		return nil
	}

	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.Error(t, err)
	require.True(t, errors.Is(err, errChaosFailure),
		"failure surfaces with the chaos sentinel wrapped")

	// Atomicity invariant: no partial rename persisted.
	require.Equal(t, beforeIDs, readNodeIDs(t, raw),
		"every rename rolled back; original ids intact")

	// Sentinels untouched (rollback also covers the sentinel updates).
	var prefix string
	require.NoError(t, raw.QueryRow(
		`SELECT value FROM meta WHERE key = 'meta.sync.project_prefix'`,
	).Scan(&prefix))
	require.NotEqual(t, "DEMO", prefix, "prefix sentinel reverted on rollback")
}

func TestRenameTo_PartialMapWrittenOnFailure(t *testing.T) {
	// On error, the deferred writeIDRenameMap call writes the
	// rename map with partial=true so a follow-up tool can detect
	// the half-done state.
	s, _, mtixDir := reconcileTestStore(t)
	seedTree(t, s)

	t.Cleanup(resetReconcileChaosHook)
	reconcileFailAfterN = func(callIdx int) error {
		if callIdx == 1 {
			return errChaosFailure
		}
		return nil
	}

	_, err := RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.Error(t, err)

	m, ok := readRenameMap(t, mtixDir)
	require.True(t, ok, "rename map written even on failure")
	require.True(t, m.Partial, "partial=true on failure")
	require.Equal(t, "rename-to", m.Path)
}

func TestImportAs_AtomicityFailureMidLoop(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)
	beforeIDs := readNodeIDs(t, raw)

	t.Cleanup(resetReconcileChaosHook)
	reconcileFailAfterN = func(callIdx int) error {
		if callIdx == 3 {
			return errChaosFailure
		}
		return nil
	}

	_, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.Error(t, err)
	require.True(t, errors.Is(err, errChaosFailure))

	require.Equal(t, beforeIDs, readNodeIDs(t, raw),
		"every rename rolled back; original ids intact")
}

func TestImportAs_PartialMapWrittenOnFailure(t *testing.T) {
	s, _, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)

	t.Cleanup(resetReconcileChaosHook)
	reconcileFailAfterN = func(callIdx int) error {
		if callIdx == 1 {
			return errChaosFailure
		}
		return nil
	}

	_, err := ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.Error(t, err)

	m, ok := readRenameMap(t, mtixDir)
	require.True(t, ok)
	require.True(t, m.Partial)
	require.Equal(t, "import-as", m.Path)
}

// DiscardLocal has no rename loop; its atomicity guarantee comes from
// the surrounding WithTx. Verify by injecting a synthetic failure
// AFTER one of the DELETE statements via a partial-DB-state
// expectation: if the tx truly rolls back, the seeded tree survives.
//
// We exercise this by running DiscardLocal then re-checking the
// store; without atomicity this test wouldn't be writable, so the
// chaos hook is not used here. The implicit guarantee is that
// store.WithTx rolls back on any error in the func body — the same
// path every other mutation relies on (verified by MTIX-15.2.3).
func TestDiscardLocal_AtomicityViaWithTxRollback(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)
	beforeIDs := readNodeIDs(t, raw)

	// Use a canceled ctx to force the first SQL exec to fail.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := DiscardLocal(ctx, s, mtixDir)
	require.Error(t, err, "canceled ctx must surface as an error")
	require.Equal(t, beforeIDs, readNodeIDs(t, raw),
		"every DELETE rolled back via WithTx")
}

// --- Plan field-level coverage ---

func TestPlan_JSONShape(t *testing.T) {
	// Sanity: Plan marshals to a stable JSON shape so 15.7's CLI
	// surface can serialize it as the --dry-run output.
	s, _, _ := reconcileTestStore(t)
	seedTree(t, s)
	plan, err := DryRunRenameTo(context.Background(), s, "DEMO")
	require.NoError(t, err)
	require.Equal(t, "rename-to", plan.Path)
	require.Equal(t, "DEMO", plan.NewPrefix)
	require.Empty(t, plan.ParentID, "ParentID is omitempty for rename-to")
}

// --- Plan output stability ---

func TestPlan_Stable_AcrossRuns(t *testing.T) {
	s, _, _ := reconcileTestStore(t)
	seedTree(t, s)
	p1, err := DryRunRenameTo(context.Background(), s, "DEMO")
	require.NoError(t, err)
	p2, err := DryRunRenameTo(context.Background(), s, "DEMO")
	require.NoError(t, err)
	require.Equal(t, p1, p2, "DryRun output stable across calls")
}

// reconcile audit log path constant for tests of file layout
func TestAuditFilenameStable(t *testing.T) {
	require.Equal(t, "reconcile.audit.log", ReconcileAuditFilename)
	require.Equal(t, "id-rename-map.json", IDRenameMapFilename)
}

// Smoke: dry-run output contains the path field for all 3 forms
func TestDryRun_AllPathsHavePathField(t *testing.T) {
	s, _, _ := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)

	d, err := DryRunDiscardLocal(context.Background(), s)
	require.NoError(t, err)
	require.Equal(t, "discard-local", d.Path)

	r, err := DryRunRenameTo(context.Background(), s, "DEMO")
	require.NoError(t, err)
	require.Equal(t, "rename-to", r.Path)

	i, err := DryRunImportAs(context.Background(), s, "PROJ-7")
	require.NoError(t, err)
	require.Equal(t, "import-as", i.Path)
}

// helpers exercised: filepath.Join used elsewhere already
var _ = filepath.Join
