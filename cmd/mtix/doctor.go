// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

--json emits an array with exactly id, status, detail and fix for each check.
--fix performs only explicitly registered non-grant repairs and rechecks them.
Grant commands are printed for you to run yourself; they are never executed.

Setup and document-freshness checks will be registered by their future features;
they are not available in this version.`

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
	// Attach rows (setup.shim, glue.<h>, attach.failures, skew.dispatchers)
	// appear only when something is installed, marked, alerted or beating.
	deps, err := productionAttachDoctorDeps(context.Background())
	if err != nil {
		return nil, err
	}
	if err := registerAttachDoctorChecks(context.Background(), svc, deps); err != nil {
		return nil, fmt.Errorf("register attach doctor checks: %w", err)
	}
	return svc, nil
}
