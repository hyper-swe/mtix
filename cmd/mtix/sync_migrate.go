// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// PhaseReport is one phase's outcome in the migration orchestration
// report (ADR-003 §7). The orchestrator runs the phases in order and
// records each here so both humans and agents see exactly what happened.
type PhaseReport struct {
	Phase   string `json:"phase"`
	Status  string `json:"status"` // "ok" | "deferred" | "skipped" | "noop"
	Detail  string `json:"detail,omitempty"`
	Applied bool   `json:"applied,omitempty"` // a live-store mutation happened
}

// MigrateReport aggregates the phase outcomes for `mtix sync migrate`.
// RegistryIndex is the node-number registry index as pg_index records it
// at the end of the run: present, valid and ready (MTIX-95.44).
type MigrateReport struct {
	Project       string                        `json:"project"`
	DryRun        bool                          `json:"dry_run"`
	Phases        []PhaseReport                 `json:"phases"`
	RemapsToApply int                           `json:"remaps_to_apply"`
	RegistryIndex *transport.RegistryIndexState `json:"registry_index,omitempty"`
}

// newSyncMigrateCmd creates `mtix sync migrate` — the driver for the
// ADR-003 §7 phased node-identity migration:
//
//	Phase 0   backfill node uids in the LOCAL store (Store.BackfillUIDs)
//	Phase 1   hub dedup sweep under the single-flight advisory lock
//	Phase 1.5 version-gated add of the partial unique index
//	Phase 2   dual resolution (already delivered by 30.6 — reported)
//	Phase 3   cutover readiness (gated by the version gate — reported)
//
// SAFETY: Phase 1 records renumber remaps that MOVE display numbers on a
// live hub. That mutation is applied only with explicit --yes. Without
// --yes the command runs every read-only/idempotent step and PREVIEWS
// the renumbers, exiting without recording any remap (default-to-dry-run,
// matching `mtix sync reconcile`).
func newSyncMigrateCmd() *cobra.Command {
	var yes, insecureTLS bool
	var project string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Drive the ADR-003 §7 node-identity migration phases",
		Long: `Orchestrate the distributed node-identity migration (ADR-003 §7):

  Phase 0   backfill node uids in the local store
  Phase 1   hub dedup sweep (resolve pre-existing duplicate numbers)
  Phase 1.5 add the registry unique index (version-gated)
  Phase 2   dual resolution status (delivered by the dual-carry transition)
  Phase 3   cutover readiness (gated on every client being remap-aware)

Phase 1 MOVES display numbers on the hub when duplicates exist. Without
--yes the command PREVIEWS the renumbers and applies nothing. Re-run with
--yes to record the remaps to the live store.

The registry index covers every project on the hub, so Phase 1 records
the duplicate creates of every project, whichever project --project
names. Phase 1.5 builds the index while the version gate is open, and
leaves the recorded duplicate creates out of it by event id: they stay in
the event log unchanged. It drops an index that is not valid or not ready
and builds it again, and never reports such an index present. It refuses
before the build, with the count and the limit, when the hub holds more
duplicate creates than the index can leave out. Only the table owner can
build the index. --json reports the index state as registry_index.`,
		Args: syncExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncMigrate(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, project, yes)
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false,
		"Apply the Phase 1 renumber remaps to the live hub (required to mutate)")
	cmd.Flags().StringVar(&project, "project", "",
		"Project prefix to migrate (defaults to the local project)")
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes only when every host the connection may use is loopback or a local socket (development only)")
	return cmd
}

// migrateHub is the subset of *transport.Pool the orchestrator needs.
// Narrowing to an interface keeps the phase logic unit-testable with a
// fake hub (no live PG) and documents exactly which hub operations the
// migration drives.
type migrateHub interface {
	SweepDuplicates(ctx context.Context, project string) (transport.SweepReport, error)
	PreviewDuplicates(ctx context.Context, project string) (int, error)
	EnsureRegistryIndex(ctx context.Context, project string) (transport.IndexResult, error)
	RegistryIndex(ctx context.Context) (transport.RegistryIndexState, error)
	ProjectUIDCutoverReady(ctx context.Context, project string) (bool, error)
}

func runSyncMigrate(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, project string, yes bool,
) error {
	if app.mtixDir == "" || app.store == nil {
		return fmt.Errorf("mtix sync migrate: not in an mtix project (run 'mtix init' first)")
	}

	// Phase 0 (local): backfill node uids. Idempotent — safe to run
	// every invocation, including dry-run, since it only fills NULL uids
	// in the local store (no hub mutation, ADR-003 §7 Phase 0).
	if err := app.store.BackfillUIDs(ctx); err != nil {
		return wrapSyncErr(stderr, "phase0 backfill", err)
	}

	prefix, err := migrateProjectPrefix(ctx, project)
	if err != nil {
		return wrapSyncErr(stderr, "resolve project", err)
	}

	dsn, err := resolveSyncDSN(args)
	if err != nil {
		return wrapSyncErr(stderr, "dsn", err)
	}
	pool, err := transport.New(ctx, dsn, opts)
	if err != nil {
		return wrapSyncErr(stderr, "connect", err)
	}
	defer pool.Close()

	report, err := orchestrateMigration(ctx, pool, prefix, yes)
	if err != nil {
		return wrapSyncErr(stderr, "orchestrate", err)
	}

	if app.jsonOutput {
		body, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(stdout, string(body))
	} else {
		printMigrateReport(stdout, report)
	}
	return nil
}

// orchestrateMigration runs Phases 1, 1.5, 2, 3 against the hub and
// returns a structured report. Phase 0 (local backfill) is the caller's
// responsibility — it touches the local store, not the hub. Splitting
// this out lets the phase sequencing be unit-tested against a fake hub.
//
// When yes is false the function PREVIEWS Phase 1 (counts the duplicate
// losers without recording any remap) and stops before Phase 1.5 — a
// preview must never mutate. When yes is true it applies the sweep then
// attempts the version-gated index build, which leaves the recorded
// duplicate creates out (MTIX-95.44).
func orchestrateMigration(ctx context.Context, hub migrateHub, prefix string, yes bool) (MigrateReport, error) {
	report := MigrateReport{Project: prefix, DryRun: !yes}
	report.Phases = append(report.Phases, PhaseReport{
		Phase: "0-backfill", Status: "ok",
		Detail: "local node uids backfilled (idempotent)",
	})

	if !yes {
		return previewMigration(ctx, hub, prefix, report)
	}

	// Phase 1 (apply): the dedup sweep records remaps + conflicts, in
	// every project on the hub (MTIX-95.44).
	sweep, err := hub.SweepDuplicates(ctx, prefix)
	if err != nil {
		return MigrateReport{}, err
	}
	report.RemapsToApply = sweep.Resolved
	p1 := PhaseReport{Phase: "1-sweep", Status: "noop", Detail: "no duplicate numbers on the hub — nothing to renumber"}
	if sweep.Resolved > 0 {
		p1 = PhaseReport{Phase: "1-sweep", Status: "ok", Applied: true,
			Detail: fmt.Sprintf("renumbered %d duplicate number(s) in %d project(s) on the hub; see 'mtix sync conflicts'",
				sweep.Resolved, len(sweep.Projects))}
	}
	report.Phases = append(report.Phases, p1)

	// Phase 1.5 (apply): version-gated index build. Phase 1 has just run,
	// so every duplicate create is recorded and the build leaves it out.
	idx, err := hub.EnsureRegistryIndex(ctx, prefix)
	if err != nil {
		return MigrateReport{}, err
	}
	report.RegistryIndex = &idx.State
	report.Phases = append(report.Phases, indexPhaseReport(idx))

	report.Phases = appendDualAndCutover(ctx, hub, prefix, report.Phases)
	return report, nil
}

// previewMigration is the dry run: it previews Phase 1 and reports the
// registry index as it is, naming the fix when it is not valid or not
// ready (MTIX-95.44). It mutates nothing.
func previewMigration(ctx context.Context, hub migrateHub, prefix string, report MigrateReport) (MigrateReport, error) {
	n, err := hub.PreviewDuplicates(ctx, prefix)
	if err != nil {
		return MigrateReport{}, err
	}
	state, err := hub.RegistryIndex(ctx)
	if err != nil {
		return MigrateReport{}, err
	}
	report.RemapsToApply = n
	report.RegistryIndex = &state
	status, detail := "noop", "no duplicate numbers — sweep would be a no-op"
	if n > 0 {
		status = "deferred"
		detail = fmt.Sprintf("%d duplicate number(s) would be renumbered on the hub — re-run with --yes to apply", n)
	}
	indexDetail := "deferred until Phase 1 is applied (--yes)"
	if state.NotUsable() {
		indexDetail = registryIndexNotUsable(state) + "; " + registryIndexRebuild
	}
	report.Phases = append(report.Phases,
		PhaseReport{Phase: "1-sweep", Status: status, Detail: detail},
		PhaseReport{Phase: "1.5-index", Status: "skipped", Detail: indexDetail},
	)
	report.Phases = appendDualAndCutover(ctx, hub, prefix, report.Phases)
	return report, nil
}

// indexPhaseReport renders the Phase 1.5 outcome. It reports the registry
// index already present only when the result shows it valid and ready,
// and names the fix for an index that is not (MTIX-95.44).
func indexPhaseReport(idx transport.IndexResult) PhaseReport {
	switch {
	case !idx.GateOpen:
		detail := "version gate closed — an active client is below the remap-aware minimum"
		if idx.State.NotUsable() {
			detail += "; " + registryIndexNotUsable(idx.State) + "; " + registryIndexRebuild
		}
		return PhaseReport{Phase: "1.5-index", Status: "deferred", Detail: detail}
	case idx.Added:
		return PhaseReport{Phase: "1.5-index", Status: "ok", Applied: true, Detail: indexAddedDetail(idx)}
	case idx.State.Usable():
		return PhaseReport{Phase: "1.5-index", Status: "noop", Detail: "registry index already present"}
	default:
		return PhaseReport{Phase: "1.5-index", Status: "deferred",
			Detail: "the node-number registry index is not valid and ready; " + registryIndexRebuild}
	}
}

// indexAddedDetail describes a built index: rebuilt when it replaced one
// that was not valid or not ready, and the duplicate creates it leaves out
// (MTIX-95.44).
func indexAddedDetail(idx transport.IndexResult) string {
	detail := fmt.Sprintf("registry unique index added over %d create rows", idx.CreateCount)
	if idx.Rebuilt {
		detail = fmt.Sprintf("registry index was not valid or not ready: dropped and built again over %d create rows",
			idx.CreateCount)
	}
	if idx.LeftOut > 0 {
		detail += fmt.Sprintf(", leaving out %d duplicate create(s) the sweep recorded (they stay in the event log)",
			idx.LeftOut)
	}
	return detail
}

// appendDualAndCutover adds the Phase 2 (always-on dual resolution) and
// Phase 3 (cutover readiness) report rows. Both are read-only.
func appendDualAndCutover(ctx context.Context, hub migrateHub, prefix string, phases []PhaseReport) []PhaseReport {
	phases = append(phases, PhaseReport{Phase: "2-dual-resolution", Status: "ok",
		Detail: "dual-carry active: events carry uid + node_id; apply prefers uid, falls back to node_id"})

	ready, err := hub.ProjectUIDCutoverReady(ctx, prefix)
	switch {
	case err != nil:
		phases = append(phases, PhaseReport{Phase: "3-cutover", Status: "deferred",
			Detail: "cutover readiness unknown: " + scrubSyncText(err.Error())})
	case ready:
		phases = append(phases, PhaseReport{Phase: "3-cutover", Status: "ok",
			Detail: "every active client is remap-aware — cutover to uid-keyed events is eligible"})
	default:
		phases = append(phases, PhaseReport{Phase: "3-cutover", Status: "deferred",
			Detail: "cutover held: not every active client is remap-aware (liveness gate)"})
	}
	return phases
}

// migrateProjectPrefix resolves the project prefix: an explicit --project
// override, else the local meta.sync.project_prefix.
func migrateProjectPrefix(ctx context.Context, override string) (string, error) {
	if override != "" {
		return override, nil
	}
	var prefix string
	if err := app.store.QueryRow(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.project_prefix'`,
	).Scan(&prefix); err != nil {
		return "", fmt.Errorf("read local project prefix (pass --project): %w", err)
	}
	if prefix == "" {
		return "", fmt.Errorf("local project prefix is empty (pass --project)")
	}
	return prefix, nil
}

func printMigrateReport(w io.Writer, r MigrateReport) {
	mode := "APPLY"
	if r.DryRun {
		mode = "DRY RUN (re-run with --yes to apply Phase 1)"
	}
	fmt.Fprintf(w, "migration orchestration for %s — %s\n\n", r.Project, mode)
	for _, p := range r.Phases {
		marker := "  "
		if p.Applied {
			marker = "* "
		}
		fmt.Fprintf(w, "%s[%-9s] %-18s %s\n", marker, p.Status, p.Phase, p.Detail)
	}
}
