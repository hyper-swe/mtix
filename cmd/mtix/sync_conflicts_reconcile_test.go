// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// PG-free CLI tests for mtix sync conflicts list/resolve and mtix
// sync reconcile.

// --- conflicts ---

func TestSyncConflictsCmd_Construction(t *testing.T) {
	cmd := newSyncConflictsCmd()
	require.Equal(t, "conflicts", cmd.Use)

	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	require.True(t, subs["list"])
	require.True(t, subs["resolve"])
}

func TestSyncConflictsListCmd_Flags(t *testing.T) {
	cmd := newSyncConflictsListCmd()
	require.NotNil(t, cmd.Flags().Lookup("node"))
	require.NotNil(t, cmd.Flags().Lookup("batch"))
}

func TestSyncConflictsResolveCmd_Flags(t *testing.T) {
	cmd := newSyncConflictsResolveCmd()
	require.NotNil(t, cmd.Flags().Lookup("action"))
}

func TestRunSyncConflictsList_RefusesOutsideMtixProject(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = ""
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncConflictsList(context.Background(), &stdout, &stderr, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

func TestRunSyncConflictsResolve_InvalidAction(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = t.TempDir()
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncConflictsResolve(context.Background(), &stdout, &stderr,
		"1", "garbage")
	require.Error(t, err)
	require.Contains(t, err.Error(), "--action must be one of")
}

func TestRunSyncConflictsResolve_NonNumericConflictID(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = t.TempDir()
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncConflictsResolve(context.Background(), &stdout, &stderr,
		"not-an-int", "keep-local")
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be an integer")
}

func TestValidResolveActions(t *testing.T) {
	// Sanity: the canonical 4 actions are present.
	for _, a := range []string{"keep-local", "keep-remote", "both-renumbered", "acknowledge"} {
		require.Truef(t, validResolveActions[a], "%s missing from valid set", a)
	}
	// Negative.
	require.False(t, validResolveActions["nonsense"])
	require.False(t, validResolveActions[""])
}

func TestPrintConflictsTable_Empty(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, printConflictsTable(&buf, nil))
	require.Contains(t, buf.String(), "no unresolved conflicts")
}

func TestPrintConflictsTable_LowCountNoBanner(t *testing.T) {
	rows := []ConflictRow{
		{ConflictID: 1, NodeID: "MTIX-1", Resolution: "lww",
			EventIDWinner: "abc-winner", EventIDLoser: "def-loser"},
	}
	var buf bytes.Buffer
	require.NoError(t, printConflictsTable(&buf, rows))
	out := buf.String()
	require.Contains(t, out, "[1] MTIX-1")
	require.NotContains(t, out, "Use 'mtix sync conflicts list --batch")
}

func TestPrintConflictsTable_HighCountBanner(t *testing.T) {
	rows := make([]ConflictRow, 51)
	for i := range rows {
		rows[i] = ConflictRow{ConflictID: int64(i + 1), NodeID: "MTIX-1", Resolution: "lww"}
	}
	var buf bytes.Buffer
	require.NoError(t, printConflictsTable(&buf, rows))
	require.Contains(t, buf.String(), "51 unresolved conflicts")
	require.Contains(t, buf.String(), "--batch <node-id>")
}

func TestNullIfEmpty(t *testing.T) {
	require.Nil(t, nullIfEmpty(""))
	require.Equal(t, "abc", nullIfEmpty("abc"))
}

// --- reconcile ---

func TestSyncReconcileCmd_Construction(t *testing.T) {
	cmd := newSyncReconcileCmd()
	require.Equal(t, "reconcile", cmd.Use)
	for _, name := range []string{"discard-local", "rename-to", "import-as", "dry-run", "yes"} {
		require.NotNilf(t, cmd.Flags().Lookup(name), "%s flag declared", name)
	}
}

func TestSyncCmd_RegistersAllNineFR18Commands(t *testing.T) {
	cmd := newSyncCmd()
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	expected := []string{
		"init", "clone", "push", "pull", "status", "doctor",
		"conflicts", "reconcile",
	}
	for _, name := range expected {
		require.Truef(t, subs[name], "%s subcommand registered", name)
	}
}

func TestRunSyncReconcile_RefusesOutsideMtixProject(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = ""
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncReconcile(context.Background(), &stdout, &stderr,
		reconcileFlags{discardLocal: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

func TestRunSyncReconcile_RequiresExactlyOnePath(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = t.TempDir()
	t.Cleanup(func() { app.mtixDir = saved })

	cases := []struct {
		name string
		f    reconcileFlags
	}{
		{"none", reconcileFlags{}},
		{"two", reconcileFlags{discardLocal: true, renameTo: "DEMO"}},
		{"three", reconcileFlags{discardLocal: true, renameTo: "DEMO", importAs: "PROJ-7"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runSyncReconcile(context.Background(), &stdout, &stderr, tc.f)
			require.Error(t, err)
			require.Contains(t, err.Error(), "exactly one of")
		})
	}
}

func TestReconcileFlags_PathCount(t *testing.T) {
	require.Equal(t, 0, reconcileFlags{}.pathCount())
	require.Equal(t, 1, reconcileFlags{discardLocal: true}.pathCount())
	require.Equal(t, 1, reconcileFlags{renameTo: "DEMO"}.pathCount())
	require.Equal(t, 1, reconcileFlags{importAs: "PROJ-7"}.pathCount())
	require.Equal(t, 2, reconcileFlags{discardLocal: true, renameTo: "DEMO"}.pathCount())
}

func TestPrintReconcilePlan_AutoDryRunHeader(t *testing.T) {
	var buf bytes.Buffer
	plan := sqlite.Plan{
		Path: "rename-to", NewPrefix: "DEMO", NodeCount: 3,
		Renames: []sqlite.Rename{{OldID: "MTIX-1", NewID: "DEMO-1"}},
	}
	printReconcilePlan(&buf, plan, true)
	require.Contains(t, buf.String(), "DRY RUN")
	require.Contains(t, buf.String(), "--yes to execute")
	require.Contains(t, buf.String(), "MTIX-1 -> DEMO-1")
}

func TestPrintReconcilePlan_NoDryRunHeader(t *testing.T) {
	var buf bytes.Buffer
	plan := sqlite.Plan{
		Path: "import-as", ParentID: "PROJ-7", NodeCount: 5,
	}
	printReconcilePlan(&buf, plan, false)
	out := buf.String()
	require.NotContains(t, out, "DRY RUN")
	require.Contains(t, out, "import-as")
	require.Contains(t, out, "PROJ-7")
}
