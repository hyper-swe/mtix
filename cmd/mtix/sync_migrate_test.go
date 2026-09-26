// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// fakeHub is a deterministic in-memory migrateHub so the phase
// orchestration is unit-testable with no live PG.
type fakeHub struct {
	previewN   int
	sweep      transport.SweepReport
	sweepErr   error
	idx        transport.IndexResult
	idxErr     error
	cutover    bool
	cutoverErr error
	state      transport.RegistryIndexState
	stateErr   error

	sweepCalls   int
	previewCalls int
	idxCalls     int
}

func (f *fakeHub) SweepDuplicates(_ context.Context, _ string) (transport.SweepReport, error) {
	f.sweepCalls++
	return f.sweep, f.sweepErr
}

func (f *fakeHub) PreviewDuplicates(_ context.Context, _ string) (int, error) {
	f.previewCalls++
	return f.previewN, nil
}

func (f *fakeHub) EnsureRegistryIndex(_ context.Context, _ string) (transport.IndexResult, error) {
	f.idxCalls++
	return f.idx, f.idxErr
}

func (f *fakeHub) RegistryIndex(_ context.Context) (transport.RegistryIndexState, error) {
	return f.state, f.stateErr
}

func (f *fakeHub) ProjectUIDCutoverReady(_ context.Context, _ string) (bool, error) {
	return f.cutover, f.cutoverErr
}

func phaseByName(r MigrateReport, name string) (PhaseReport, bool) {
	for _, p := range r.Phases {
		if p.Phase == name {
			return p, true
		}
	}
	return PhaseReport{}, false
}

// DRY-RUN: previews Phase 1, never sweeps, defers the index.
func TestOrchestrate_DryRunPreviewsAndDoesNotMutate(t *testing.T) {
	hub := &fakeHub{previewN: 2}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", false)
	require.NoError(t, err)

	require.True(t, rep.DryRun)
	require.Equal(t, 2, rep.RemapsToApply)
	require.Equal(t, 0, hub.sweepCalls, "dry-run must NOT sweep (no mutation)")
	require.Equal(t, 0, hub.idxCalls, "dry-run must NOT add the index")
	require.Equal(t, 1, hub.previewCalls)

	p1, ok := phaseByName(rep, "1-sweep")
	require.True(t, ok)
	require.Equal(t, "deferred", p1.Status)
	idx, ok := phaseByName(rep, "1.5-index")
	require.True(t, ok)
	require.Equal(t, "skipped", idx.Status)
}

// DRY-RUN clean project: preview reports a no-op.
func TestOrchestrate_DryRunCleanProjectNoOp(t *testing.T) {
	hub := &fakeHub{previewN: 0}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", false)
	require.NoError(t, err)
	p1, _ := phaseByName(rep, "1-sweep")
	require.Equal(t, "noop", p1.Status)
}

// APPLY happy path: sweep resolves, index added, cutover ready.
func TestOrchestrate_ApplyResolvesAndAddsIndex(t *testing.T) {
	hub := &fakeHub{
		sweep:   transport.SweepReport{Resolved: 1},
		idx:     transport.IndexResult{GateOpen: true, Added: true, CreateCount: 5},
		cutover: true,
	}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.NoError(t, err)
	require.False(t, rep.DryRun)
	require.Equal(t, 1, hub.sweepCalls)
	require.Equal(t, 1, hub.idxCalls)

	p1, _ := phaseByName(rep, "1-sweep")
	require.Equal(t, "ok", p1.Status)
	require.True(t, p1.Applied)
	idx, _ := phaseByName(rep, "1.5-index")
	require.Equal(t, "ok", idx.Status)
	require.True(t, idx.Applied)
	cut, _ := phaseByName(rep, "3-cutover")
	require.Equal(t, "ok", cut.Status)
}

// APPLY clean project: sweep is a no-op but index still added when gate open.
func TestOrchestrate_ApplyCleanProjectSweepNoOpIndexAdded(t *testing.T) {
	hub := &fakeHub{
		sweep: transport.SweepReport{Resolved: 0},
		idx:   transport.IndexResult{GateOpen: true, Added: true},
	}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.NoError(t, err)
	p1, _ := phaseByName(rep, "1-sweep")
	require.Equal(t, "noop", p1.Status)
	idx, _ := phaseByName(rep, "1.5-index")
	require.Equal(t, "ok", idx.Status)
}

// APPLY gate closed: index deferred, cutover deferred.
func TestOrchestrate_ApplyGateClosedDefersIndexAndCutover(t *testing.T) {
	hub := &fakeHub{
		sweep:   transport.SweepReport{Resolved: 0},
		idx:     transport.IndexResult{GateOpen: false},
		cutover: false,
	}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.NoError(t, err)
	idx, _ := phaseByName(rep, "1.5-index")
	require.Equal(t, "deferred", idx.Status)
	cut, _ := phaseByName(rep, "3-cutover")
	require.Equal(t, "deferred", cut.Status)
}

// ERROR propagation: a dirty-log index error (Phase 1.5 before Phase 1 on
// a real hub) bubbles up so the operator sees it loudly.
func TestOrchestrate_IndexErrorPropagates(t *testing.T) {
	hub := &fakeHub{
		sweep:  transport.SweepReport{Resolved: 0},
		idxErr: fmt.Errorf("build index: duplicate key value"),
	}
	_, err := orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.Error(t, err)
}

// Dual resolution is always reported (Phase 2 delivered by 30.6).
func TestOrchestrate_DualResolutionAlwaysReported(t *testing.T) {
	hub := &fakeHub{idx: transport.IndexResult{GateOpen: true}}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.NoError(t, err)
	p2, ok := phaseByName(rep, "2-dual-resolution")
	require.True(t, ok)
	require.Equal(t, "ok", p2.Status)
}

// A cutover-readiness query error degrades the Phase 3 row to deferred
// (read-only signal; never fails the whole migration).
func TestOrchestrate_CutoverQueryErrorDefersPhase3(t *testing.T) {
	hub := &fakeHub{
		idx:        transport.IndexResult{GateOpen: true},
		cutoverErr: fmt.Errorf("gate query failed"),
	}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.NoError(t, err)
	cut, _ := phaseByName(rep, "3-cutover")
	require.Equal(t, "deferred", cut.Status)
}

// A sweep error during apply bubbles up.
func TestOrchestrate_SweepErrorPropagates(t *testing.T) {
	hub := &fakeHub{sweepErr: fmt.Errorf("advisory lock failed")}
	_, err := orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.Error(t, err)
}

// migrateProjectPrefix: explicit override wins.
func TestMigrateProjectPrefix_OverrideWins(t *testing.T) {
	initTestApp(t)
	got, err := migrateProjectPrefix(context.Background(), "OVERRIDE")
	require.NoError(t, err)
	require.Equal(t, "OVERRIDE", got)
}

// migrateProjectPrefix: falls back to the stored local prefix.
func TestMigrateProjectPrefix_FromLocalMeta(t *testing.T) {
	initTestApp(t)
	_, err := app.store.WriteDB().ExecContext(context.Background(),
		`INSERT INTO meta (key, value) VALUES ('meta.sync.project_prefix', 'LOCALP')
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`)
	require.NoError(t, err)

	got, err := migrateProjectPrefix(context.Background(), "")
	require.NoError(t, err)
	require.Equal(t, "LOCALP", got)
}

// runSyncMigrate refuses outside an mtix project.
func TestRunSyncMigrate_RefusesOutsideProject(t *testing.T) {
	saved, savedStore := app.mtixDir, app.store
	app.mtixDir, app.store = "", nil
	t.Cleanup(func() { app.mtixDir, app.store = saved, savedStore })

	var stdout, stderr bytes.Buffer
	err := runSyncMigrate(context.Background(), &stdout, &stderr, nil,
		transport.Options{InsecureTLS: true}, "MTIX", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

// printMigrateReport renders both dry-run and apply modes without panic
// and includes phase markers.
func TestPrintMigrateReport_RendersBothModes(t *testing.T) {
	rep := MigrateReport{
		Project: "MTIX", DryRun: true,
		Phases: []PhaseReport{
			{Phase: "0-backfill", Status: "ok"},
			{Phase: "1-sweep", Status: "ok", Applied: true, Detail: "renumbered 1"},
		},
	}
	var buf bytes.Buffer
	printMigrateReport(&buf, rep)
	out := buf.String()
	require.Contains(t, out, "DRY RUN")
	require.Contains(t, out, "1-sweep")
	require.Contains(t, out, "* ", "applied phase carries the marker")

	rep.DryRun = false
	var buf2 bytes.Buffer
	printMigrateReport(&buf2, rep)
	require.Contains(t, buf2.String(), "APPLY")
}

// TestIndexPhaseReport_IndexNotConfirmedValid_NeverReportsPresent: an index
// result that neither added the index nor shows it valid and ready never
// reports the registry index present (MTIX-95.44).
func TestIndexPhaseReport_IndexNotConfirmedValid_NeverReportsPresent(t *testing.T) {
	got := indexPhaseReport(transport.IndexResult{GateOpen: true})
	require.NotContains(t, got.Detail, "already present")
	require.NotEqual(t, "noop", got.Status)
	require.Contains(t, got.Detail, "mtix sync migrate --yes", "the detail names the fix")
}

// Registry index states the fake hub reports (MTIX-95.44).
var (
	usableIndex    = transport.RegistryIndexState{Present: true, Valid: true, Ready: true, SchemaIdent: "public"}
	notUsableIndex = transport.RegistryIndexState{Present: true, SchemaIdent: "public"}
)

// TestIndexPhaseReport_EachResult_ReportsTheIndexState: the Phase 1.5 row
// says "already present" only for a valid and ready index, names the fix
// for one that is not, and describes a build, a rebuild and the duplicate
// creates it leaves out (MTIX-95.44).
func TestIndexPhaseReport_EachResult_ReportsTheIndexState(t *testing.T) {
	tests := []struct {
		name       string
		idx        transport.IndexResult
		wantStatus string
		want       []string
		notWant    []string
	}{
		{"valid and ready", transport.IndexResult{GateOpen: true, State: usableIndex},
			"noop", []string{"registry index already present"}, nil},
		{"not valid, gate open", transport.IndexResult{GateOpen: true, State: notUsableIndex},
			"deferred", []string{"not valid", "mtix sync migrate --yes"}, []string{"already present"}},
		{"not valid, gate closed", transport.IndexResult{State: notUsableIndex},
			"deferred", []string{"version gate closed", "public.sync_events_node_registry_uidx is not valid",
				"mtix sync migrate --yes"}, []string{"already present"}},
		{"absent, gate closed", transport.IndexResult{},
			"deferred", []string{"version gate closed"}, []string{"not valid", "already present"}},
		{"built", transport.IndexResult{GateOpen: true, Added: true, CreateCount: 7, State: usableIndex},
			"ok", []string{"registry unique index added over 7 create rows"}, []string{"leaving out", "built again"}},
		{"built leaving duplicates out", transport.IndexResult{GateOpen: true, Added: true, CreateCount: 7,
			LeftOut: 2, State: usableIndex},
			"ok", []string{"registry unique index added over 7 create rows", "leaving out 2 duplicate create(s)"}, nil},
		{"rebuilt", transport.IndexResult{GateOpen: true, Added: true, Rebuilt: true, CreateCount: 3, State: usableIndex},
			"ok", []string{"dropped and built again over 3 create rows"}, []string{"already present"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := indexPhaseReport(tt.idx)
			require.Equal(t, "1.5-index", got.Phase)
			require.Equal(t, tt.wantStatus, got.Status)
			require.Equal(t, tt.wantStatus == "ok", got.Applied)
			for _, w := range tt.want {
				require.Contains(t, got.Detail, w)
			}
			for _, w := range tt.notWant {
				require.NotContains(t, got.Detail, w)
			}
		})
	}
}

// TestOrchestrate_RegistryIndexState_ReportedInEveryMode: the report
// carries the registry index state, and the dry run names an index that is
// not valid with its fix (MTIX-95.44).
func TestOrchestrate_RegistryIndexState_ReportedInEveryMode(t *testing.T) {
	hub := &fakeHub{state: notUsableIndex}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", false)
	require.NoError(t, err)
	require.Equal(t, &notUsableIndex, rep.RegistryIndex)
	idx, _ := phaseByName(rep, "1.5-index")
	require.Equal(t, "skipped", idx.Status)
	require.Contains(t, idx.Detail, "not valid")
	require.Contains(t, idx.Detail, "mtix sync migrate --yes")

	hub = &fakeHub{state: usableIndex}
	rep, err = orchestrateMigration(context.Background(), hub, "MTIX", false)
	require.NoError(t, err)
	idx, _ = phaseByName(rep, "1.5-index")
	require.Equal(t, "deferred until Phase 1 is applied (--yes)", idx.Detail)

	hub = &fakeHub{idx: transport.IndexResult{GateOpen: true, State: usableIndex}}
	rep, err = orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.NoError(t, err)
	require.Equal(t, &usableIndex, rep.RegistryIndex)

	hub = &fakeHub{stateErr: fmt.Errorf("catalog read failed")}
	_, err = orchestrateMigration(context.Background(), hub, "MTIX", false)
	require.Error(t, err, "a dry run that cannot read the index state fails")
}

// TestOrchestrate_SweepAcrossProjects_NamesTheProjectCount: the Phase 1 row
// counts the projects the hub-wide sweep recorded losers in (MTIX-95.44).
func TestOrchestrate_SweepAcrossProjects_NamesTheProjectCount(t *testing.T) {
	hub := &fakeHub{sweep: transport.SweepReport{Resolved: 3, Projects: []string{"LEG", "MTIX"}},
		idx: transport.IndexResult{GateOpen: true, State: usableIndex}}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.NoError(t, err)
	p1, _ := phaseByName(rep, "1-sweep")
	require.Equal(t, "renumbered 3 duplicate number(s) in 2 project(s) on the hub; see 'mtix sync conflicts'", p1.Detail)
}

// TestOrchestrate_IndexError_ReportsTheSweepBeforeTheError: when the index
// build is refused, the sweep of the same run has already recorded the
// duplicates, so the report still carries the sweep phase, and the index
// phase, with the error (MTIX-95.44).
func TestOrchestrate_IndexError_ReportsTheSweepBeforeTheError(t *testing.T) {
	hub := &fakeHub{sweep: transport.SweepReport{Resolved: 2, Projects: []string{"LEG"}},
		idxErr: fmt.Errorf("registry index not built: the hub holds 2 duplicate creates")}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.Error(t, err)
	p1, ok := phaseByName(rep, "1-sweep")
	require.True(t, ok, "the sweep phase is reported")
	require.True(t, p1.Applied)
	require.Contains(t, p1.Detail, "renumbered 2 duplicate number(s) in 1 project(s)")
	idx, ok := phaseByName(rep, "1.5-index")
	require.True(t, ok)
	require.Contains(t, idx.Detail, "registry index not built")
}

// TestOrchestrate_IndexNotValidAfterBuild_NamesTheFix: an index result with
// the gate open that neither built the index nor shows it valid and ready
// never reports the index present and names the fix (MTIX-95.44).
func TestOrchestrate_IndexNotValidAfterBuild_NamesTheFix(t *testing.T) {
	hub := &fakeHub{idx: transport.IndexResult{GateOpen: true, State: notUsableIndex}}
	rep, err := orchestrateMigration(context.Background(), hub, "MTIX", true)
	require.NoError(t, err)
	idx, _ := phaseByName(rep, "1.5-index")
	require.NotContains(t, idx.Detail, "already present")
	require.Contains(t, idx.Detail, "mtix sync migrate --yes")
	require.Equal(t, &notUsableIndex, rep.RegistryIndex)
}
