// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// fakeRepairHub records the calls and answers from a script.
type fakeRepairHub struct {
	calls   []string
	dryRuns []bool
	reports map[string]transport.UIDRepairReport
	err     error
}

func (f *fakeRepairHub) RepairCreateUIDs(_ context.Context, project string,
	_ map[string]string, dryRun bool,
) (transport.UIDRepairReport, error) {
	f.calls = append(f.calls, project)
	f.dryRuns = append(f.dryRuns, dryRun)
	if f.err != nil {
		return transport.UIDRepairReport{}, f.err
	}
	r := f.reports[project]
	r.Project, r.DryRun = project, dryRun
	return r, nil
}

func TestRepairUIDsForProjects_VisitsEveryPrefixInOrder(t *testing.T) {
	hub := &fakeRepairHub{reports: map[string]transport.UIDRepairReport{
		"MTIX": {Stamped: 3}, "DEMO": {Stamped: 1}, "ALPHA": {},
	}}
	uids := map[string]map[string]string{
		"MTIX": {"MTIX-1": "u1"}, "DEMO": {"DEMO-1": "u2"}, "ALPHA": {"ALPHA-1": "u3"},
	}
	reports, err := repairUIDsForProjects(context.Background(), hub, uids, true)
	require.NoError(t, err)
	assert.Equal(t, []string{"ALPHA", "DEMO", "MTIX"}, hub.calls, "stable prefix order")
	assert.Equal(t, []bool{true, true, true}, hub.dryRuns, "dry-run reaches every call")
	require.Len(t, reports, 3)
	assert.Equal(t, 3, reports[2].Stamped)
}

func TestRepairUIDsForProjects_ErrorNamesTheProject(t *testing.T) {
	hub := &fakeRepairHub{err: errors.New("boom")}
	_, err := repairUIDsForProjects(context.Background(), hub,
		map[string]map[string]string{"MTIX": {"MTIX-1": "u1"}}, false)
	require.ErrorContains(t, err, "project MTIX: boom")
}

func TestPrintUIDRepairReports_PartialRepairIsLoud(t *testing.T) {
	var stdout, stderr bytes.Buffer
	printUIDRepairReports(&stdout, &stderr, []transport.UIDRepairReport{{
		Project: "MTIX", Stamped: 2, AlreadyStamped: 5,
		HubOnly:    []string{"MTIX-7", "MTIX-8"},
		Mismatched: []transport.UIDMismatch{{NodeID: "MTIX-3", HubUID: "h", LocalUID: "l"}},
	}})
	assert.Contains(t, stdout.String(), "MTIX: stamped 2 hub create row(s); 5 already stamped, 1 mismatched, 2 hub-only")
	assert.Contains(t, stderr.String(), "2 create row(s) on the hub have no local node and stay unstamped: MTIX-7, MTIX-8")
	assert.Contains(t, stderr.String(), "repair-uids --project MTIX")
	assert.Contains(t, stderr.String(), "MTIX-3 carries uid h on the hub but l locally; left untouched")
	assert.NotContains(t, stdout.String(), "DRY RUN")
}

func TestPrintUIDRepairReports_DryRunSaysNothingWasWritten(t *testing.T) {
	var stdout, stderr bytes.Buffer
	printUIDRepairReports(&stdout, &stderr, []transport.UIDRepairReport{{Project: "MTIX", DryRun: true, Stamped: 4}})
	assert.Contains(t, stdout.String(), "MTIX: would stamp 4 hub create row(s)")
	assert.Contains(t, stdout.String(), "DRY RUN: nothing was written")
	assert.Empty(t, stderr.String())
}

func TestRunSyncRepairUIDs_OutsideProjectFails(t *testing.T) {
	saveAndResetApp(t)
	var stdout, stderr bytes.Buffer
	err := runSyncRepairUIDs(context.Background(), &stdout, &stderr, nil, transport.Options{}, "", false)
	require.ErrorContains(t, err, "not in an mtix project")
}

func TestRunSyncRepairUIDs_UnknownProjectFails(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))
	var stdout, stderr bytes.Buffer
	err := runSyncRepairUIDs(context.Background(), &stdout, &stderr, nil, transport.Options{}, "NOPE", false)
	require.ErrorContains(t, err, `no local nodes for project "NOPE"`)
}

func TestRunSyncRepairUIDs_EmptyStoreHasNothingToRepair(t *testing.T) {
	initTestApp(t)
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncRepairUIDs(context.Background(), &stdout, &stderr, nil, transport.Options{}, "", false))
	assert.Contains(t, stdout.String(), "no local nodes; nothing to repair")
}

func TestNewSyncRepairUIDsCmd_Flags(t *testing.T) {
	cmd := newSyncRepairUIDsCmd()
	assert.Equal(t, "repair-uids [DSN]", cmd.Use)
	require.NotNil(t, cmd.Flags().Lookup("dry-run"))
	require.NotNil(t, cmd.Flags().Lookup("project"))
	assert.Contains(t, cmd.Long, "never overwritten")
	assert.Contains(t, cmd.Long, "Nothing is deleted")
}
