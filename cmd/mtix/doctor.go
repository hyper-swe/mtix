// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/redact"
)

const doctorLong = `Run registered health checks for this checkout and host.

Sync checks run only for configured transports. An unused Postgres transport is
not checked on a relay-only board. WARN and INFO are advisory: exit 0 when no
check fails, or 2 when any check reports FAIL.

--json emits an array with id, status, detail and fix for each check; a check may add
a data object of machine-readable numbers (the history.size row does), and rows
without one have exactly the four fields.
--fix performs only explicitly registered non-grant repairs and rechecks them.
Grant commands are printed for you to run yourself; they are never executed.
Without --fix, operator-local files, including attach artifacts, stay unchanged.

The read-only docs.stale check compares board-root MTIX.md with this binary
and checks live native pointers. WARN prints mtix -C <board-root> docs generate;
run that command and rerun doctor. --fix never writes these documents.
HYPERSWE.md suppresses missing native pointers, but MTIX.md is still checked.

On an opened local board, budget.rewind reports counters below their protected
mirror and retained rewind diagnostics. It never resets allowance, even with --fix.
wake.guard-trips lists recent loop-guard trips (hop depth and back-off), newest
first, with agent, chain, time and the exact mtix config set command; it is
read-only and --json carries the same rows under data.trips.
wake.ledger reports retained wake rows, claimed rows
and rejected completions. A newer dispatch protocol reports FAIL: install a
compatible binary, restart the dispatcher and rerun doctor. This read-only check
never prunes wake rows or lowers the protocol floor, including with --fix.

The read-only export.schema check compares the schema_version .mtix/tasks.json
declares with the one this binary writes. Every write re-exports the file in
this binary's schema; sync.auto_sync does not stop that. WARN prints the fix.

wake.settings reports effective host/agent wake values and sources.
settings.ignored-workspace-key reports authority keys ignored in config.yaml
and prints exact host-local config commands. These checks never change settings.

Setup checks will be registered by their future features.`

func newDoctorCmd() *cobra.Command { return newDoctorCommand(newConfiguredDoctorService) }

func newDoctorCommand(provider func() (*service.DoctorService, error)) *cobra.Command {
	var fix bool
	cmd := &cobra.Command{Use: "doctor", Short: "Run registered checkout and host health checks", Long: doctorLong,
		Args: syncExactArgs(0), SilenceErrors: true, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			svc, err := provider()
			if err != nil {
				return service.NewDoctorService(service.DoctorOptions{Scrub: scrubSyncText}).RedactError(fmt.Errorf("initialize doctor: %w", err))
			}
			jsonOutput, err := cmd.Flags().GetBool("json")
			if err != nil {
				return svc.RedactError(fmt.Errorf("read doctor JSON flag: %w", err))
			}
			return runDoctor(cmd.Context(), cmd.OutOrStdout(), svc, doctorOutputOptions{Fix: fix, JSON: jsonOutput})
		}}
	cmd.Flags().BoolVar(&fix, "fix", false, "Perform explicitly registered non-grant repairs, then recheck")
	return cmd
}

type doctorOutputOptions struct{ Fix, JSON bool }

func runDoctor(ctx context.Context, out io.Writer, svc *service.DoctorService, opts doctorOutputOptions) (err error) {
	defer func() { err = svc.RedactError(err) }()
	rows, runErr := svc.Run(ctx, service.DoctorRunOptions{Fix: opts.Fix})
	if runErr != nil && !errors.Is(runErr, service.ErrDoctorChecksFailed) {
		return fmt.Errorf("run doctor: %w", runErr)
	}
	if err := writeDoctorRows(out, rows, opts.JSON); err != nil {
		return err
	}
	if runErr != nil {
		return fmt.Errorf("run doctor: %w", runErr)
	}
	return nil
}

func writeDoctorRows(out io.Writer, rows []service.DoctorResult, jsonOutput bool) error {
	if jsonOutput {
		if err := json.NewEncoder(out).Encode(rows); err != nil {
			return fmt.Errorf("write doctor JSON: %w", err)
		}
		return nil
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(out, "[%s] %s %s\n", strings.ToUpper(string(row.Status)), row.ID, row.Detail); err != nil {
			return fmt.Errorf("write doctor result: %w", err)
		}
		if row.Fix != "" {
			if _, err := fmt.Fprintf(out, "       fix: %s\n", row.Fix); err != nil {
				return fmt.Errorf("write doctor fix: %w", err)
			}
		}
	}
	if len(rows) == 0 {
		if _, err := fmt.Fprintln(out, "No checks apply to this checkout and host."); err != nil {
			return fmt.Errorf("write empty doctor report: %w", err)
		}
	}
	return nil
}

func newConfiguredDoctorService() (*service.DoctorService, error) {
	known := knownSyncDSNs()
	opts := service.DoctorOptions{Platform: runtime.GOOS, Scrub: func(text string) string { return redact.Known(text, known...) }}
	if app.mtixDir != "" {
		request := configuredDoctorCollection()
		if request.Postgres || request.Relay {
			opts.CollectSync = func(ctx context.Context) ([]service.DoctorResult, error) {
				return doctorSyncRows(collectSyncDoctor(ctx, request)), nil
			}
		}
	}
	svc := service.NewDoctorService(opts)
	if err := registerHistorySizeCheck(svc); err != nil { // MTIX-95.31.19.4
		return nil, fmt.Errorf("register history size check: %w", err)
	}
	if err := registerExportSchemaCheck(svc); err != nil { // MTIX-168
		return nil, fmt.Errorf("register export schema check: %w", err)
	}
	if err := registerServeExposureCheck(svc); err != nil { // MTIX-220
		return nil, fmt.Errorf("register serve exposure check: %w", err)
	}
	if err := registerDaemonLogsCheck(svc); err != nil {
		return nil, fmt.Errorf("register daemon logs check: %w", err)
	}
	if err := svc.Register(documentFreshnessCheck(filepath.Dir(app.mtixDir))); err != nil {
		return nil, fmt.Errorf("register documentation check: %w", err)
	}
	if hooksFileExists(app.mtixDir) {
		if err := svc.Register(hooksProgramsCheck(app.mtixDir)); err != nil {
			return nil, fmt.Errorf("register hook program check: %w", err)
		}
		if err := svc.Register(hooksStrictCheck(app.mtixDir)); err != nil {
			return nil, fmt.Errorf("register hook strict check: %w", err)
		}
		if err := svc.Register(hooksOriginsCheck(app.mtixDir)); err != nil {
			return nil, fmt.Errorf("register hook origin check: %w", err)
		}
	}
	// Attach rows (setup.shim, glue.<h>, attach.failures, skew.dispatchers)
	// appear only when something is installed, marked, alerted or beating.
	deps, err := productionAttachDoctorDeps(context.Background())
	if err != nil {
		return nil, err
	}
	if err := registerAttachDoctorChecks(context.Background(), svc, deps); err != nil {
		return nil, fmt.Errorf("register attach doctor checks: %w", err)
	}
	if err := registerWakeDoctorChecks(svc); err != nil {
		return nil, fmt.Errorf("register wake doctor checks: %w", err)
	}
	return svc, nil
}

func registerWakeDoctorChecks(svc *service.DoctorService) error {
	if err := registerPermissionDoctor(svc); err != nil {
		return fmt.Errorf("register wake permission diagnostics: %w", err)
	}
	if err := registerWakeSettingsDoctor(svc); err != nil {
		return fmt.Errorf("register wake settings diagnostics: %w", err)
	}
	if err := registerConfiguredWakeBudgetDoctor(svc); err != nil {
		return fmt.Errorf("register wake budget diagnostics: %w", err)
	}
	if err := registerConfiguredWakeLedgerDoctor(svc); err != nil {
		return fmt.Errorf("register wake ledger diagnostics: %w", err)
	}
	if err := registerWakeListenerDoctor(svc); err != nil {
		return fmt.Errorf("register wake listener diagnostics: %w", err)
	}
	return nil
}
