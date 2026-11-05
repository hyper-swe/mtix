// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestDoctorGlueTrackedStore_Causes_RowsAndExactFixes(t *testing.T) {
	tests := []struct {
		name   string
		d      attach.GlueDecision
		want   bool
		fix    string
		detail string
	}{
		{"tracked store", attach.GlueDecision{Reason: attach.ReasonTrackedStore, Cause: attach.TrackedStore}, true, trackedStoreFix, "tracked by git"},
		{"tracked symlink over the store path", attach.GlueDecision{Reason: attach.ReasonTrackedStore, Cause: attach.TrackedAncestor}, true, trackedAncestorFix, "symlink or submodule"},
		{"sparse index", attach.GlueDecision{Reason: attach.ReasonIndexUnsupported, Cause: attach.CauseSparse}, true, sparseIndexFix, "sparse index"},
		{"split index base missing", attach.GlueDecision{Reason: attach.ReasonIndexUnsupported, Cause: attach.CauseSplitBase}, true, splitBaseFix, "split index"},
		{"unknown extension", attach.GlueDecision{Reason: attach.ReasonIndexUnsupported, Cause: attach.CauseExtension}, true, unsupportedFix, "version or extension"},
		{"oversize", attach.GlueDecision{Reason: attach.ReasonIndexUnsupported, Cause: attach.CauseOversize}, true, unsupportedFix, "larger"},
		{"malformed", attach.GlueDecision{Reason: attach.ReasonIndexUnsupported, Cause: attach.CauseMalformed}, true, unsupportedFix, "could not be parsed"},
		{"unlisted is not a problem", attach.GlueDecision{Reason: attach.ReasonUnlisted}, false, "", ""},
		{"attached is not a problem", attach.GlueDecision{Reason: attach.ReasonAttached}, false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.OperatorState(t)
			deps := attachDoctorTestDeps(t, st, 2)
			deps.glue = func(context.Context) attach.GlueDecision { return tt.d }
			row, ok := attachDoctorRows(t, deps, false)["glue.tracked-store"]
			require.Equal(t, tt.want, ok)
			if ok {
				assert.Equal(t, service.DoctorWarn, row.Status)
				assert.Equal(t, tt.fix, row.Fix, "the exact fix, never just mtix doctor")
				assert.Contains(t, row.Detail, tt.detail)
			}
		})
	}
}

func TestDoctorGlueTrackedStore_NoPredicate_NoRow(t *testing.T) {
	st := testutil.OperatorState(t)
	_, ok := attachDoctorRows(t, attachDoctorTestDeps(t, st, 2), false)["glue.tracked-store"]
	assert.False(t, ok)
}

func TestDoctorGlueStrandedQueue_UnlistedBoardWithRows_WarnsWithFix(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		rows   int
		want   bool
	}{
		{"unlisted with rows", attach.ReasonUnlisted, 2, true},
		{"unlisted without rows", attach.ReasonUnlisted, 0, false},
		{"attached with rows is the drain's job", attach.ReasonAttached, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := testutil.OperatorState(t)
			deps := attachDoctorTestDeps(t, st, 2)
			deps.glue = func(context.Context) attach.GlueDecision { return attach.GlueDecision{Reason: tt.reason} }
			deps.pending = func(context.Context) ([]sqlite.AttachPending, error) {
				return make([]sqlite.AttachPending, tt.rows), nil
			}
			row, ok := attachDoctorRows(t, deps, false)["glue.stranded-queue"]
			require.Equal(t, tt.want, ok)
			if ok {
				assert.Equal(t, service.DoctorWarn, row.Status)
				assert.Contains(t, row.Fix, "mtix agent register <agent-id>")
				assert.Contains(t, row.Detail, "2 attach queue row(s)")
			}
		})
	}
}

// The production doctor deps carry the real predicate and queue reader, so a
// legacy board whose store is committed shows the row without any injected fake.
func TestDoctorGlue_ProductionDeps_ShowTrackedStoreRowForCommittedLegacyBoard(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	root := legacyBoardCheckout(t, st)
	writeGitIndex(t, root, ".mtix/data/mtix.db")
	t.Chdir(root)

	deps, err := productionAttachDoctorDeps(t.Context())
	require.NoError(t, err)
	require.NotNil(t, deps.glue, "production doctor deps must carry the glue predicate")
	require.NotNil(t, deps.pending, "production doctor deps must carry the queue reader")
	row, ok := attachDoctorRows(t, deps, false)["glue.tracked-store"]
	require.True(t, ok, "committed legacy store must be reported")
	assert.Equal(t, trackedStoreFix, row.Fix)
}

// legacyBoardCheckout lists a git checkout whose store has no instance id.
func legacyBoardCheckout(t *testing.T, st *oplocal.State) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	dataDir := filepath.Join(root, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	store, err := sqlite.New(dataDir, slog.Default())
	require.NoError(t, err)
	_, err = store.WriteDB().ExecContext(t.Context(), "DELETE FROM meta WHERE key='store_instance_id'")
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, st.ListBoard(t.Context(), "11111111-1111-4111-8111-111111111111", root))
	return root
}

// writeGitIndex writes a minimal v2 git index tracking the given paths.
func writeGitIndex(t *testing.T, root string, paths ...string) {
	t.Helper()
	var b []byte
	b = append(b, "DIRC"...)
	b = append(b, 0, 0, 0, 2, 0, 0, 0, byte(len(paths)))
	for _, p := range paths {
		start := len(b)
		entry := make([]byte, 62)
		entry[27] = 0o244 // mode 100644 low byte
		entry[26] = 0o201
		entry[25] = 0
		entry[61] = byte(len(p))
		b = append(b, entry...)
		b = append(b, p...)
		b = append(b, 0)
		for (len(b)-start)%8 != 0 {
			b = append(b, 0)
		}
	}
	b = append(b, make([]byte, 20)...)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git", "index"), b, 0o644))
}
