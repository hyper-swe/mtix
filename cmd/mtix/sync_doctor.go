// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// DoctorCheck is one row in the doctor's report.
type DoctorCheck struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"`
}

// DoctorReport aggregates the 5 health checks per FR-18 / MTIX-15.7.3.
type DoctorReport struct {
	OverallPass bool          `json:"pass"`
	Checks      []DoctorCheck `json:"checks"`
}

// errDoctorChecksFailed is returned by runSyncDoctor when one or more
// health checks fail. The cobra wrapper translates this into exit
// code 2 (distinguishing failed-checks from invalid-arguments). Tests
// observe the sentinel without the process being killed.
var errDoctorChecksFailed = errors.New("doctor checks failed")

// newSyncDoctorCmd creates `mtix sync doctor`. Exits 0 if every check
// passes, exits 2 if any fails. --json output for machine consumption.
func newSyncDoctorCmd() *cobra.Command {
	var insecureTLS bool
	cmd := &cobra.Command{
		Use:   "doctor [DSN]",
		Short: "Run sync health checks (FR-18)",
		Long: `Run 5 health checks against the local store and the BYO Postgres hub:

  1. PG reachable           — opens pool + Ping
  2. Schema current         — sync_projects table exists with expected columns
  3. Queue draining         — no events older than 1h still in pending
  4. No orphan applied      — every applied_event has a matching node OR tombstone
  5. DSN secrets file mode  — .mtix/secrets is mode 0600 (when present)

Exit code: 0 on all-pass, 2 if any check fails. --json output for
agents and CI consumption.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			err := runSyncDoctor(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS})
			if errors.Is(err, errDoctorChecksFailed) {
				// Distinguish failed health checks (exit 2) from
				// invalid arguments / IO errors (exit 1, via main).
				// We've already printed the report; suppress cobra's
				// "Error:" prefix.
				cmd.SilenceErrors = true
				cmd.SilenceUsage = true
				os.Exit(2)
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes on loopback hosts (development only)")
	return cmd
}

func runSyncDoctor(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options,
) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync doctor: not in an mtix project (run 'mtix init' first)")
	}

	report := DoctorReport{OverallPass: true}

	// Try resolve DSN first; if fails, the PG checks are skipped but
	// local checks still run.
	dsn, dsnErr := resolveSyncDSN(args)

	// Check 1: PG reachable.
	if dsnErr != nil {
		report = appendCheck(report, "PG reachable", false, "DSN: "+dsnErr.Error())
	} else {
		pgOK, detail := checkPGReachable(ctx, dsn, opts)
		report = appendCheck(report, "PG reachable", pgOK, detail)
	}

	// Check 2: schema current (only meaningful if PG reachable).
	if dsnErr == nil && lastCheckPassed(report) {
		schemaOK, detail := checkSchemaCurrent(ctx, dsn, opts)
		report = appendCheck(report, "schema current", schemaOK, detail)
	} else {
		report = appendCheck(report, "schema current", false, "skipped (PG unreachable)")
	}

	// Check 3: queue draining (local only).
	if app.store == nil {
		report = appendCheck(report, "queue draining", false, "local store not initialized")
	} else {
		drainOK, detail := checkQueueDraining(ctx, app.store)
		report = appendCheck(report, "queue draining", drainOK, detail)
	}

	// Check 4: no orphan applied events (local only).
	if app.store == nil {
		report = appendCheck(report, "no orphan applied", false, "local store not initialized")
	} else {
		orphanOK, detail := checkNoOrphanApplied(ctx, app.store)
		report = appendCheck(report, "no orphan applied", orphanOK, detail)
	}

	// Check 5: DSN secrets file mode.
	modeOK, detail := checkSecretsFileMode(app.mtixDir)
	report = appendCheck(report, "secrets file mode", modeOK, detail)

	if app.jsonOutput {
		body, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(stdout, string(body))
	} else {
		printDoctorTable(stdout, report)
	}

	if !report.OverallPass {
		_ = stderr // already wrote details above
		return errDoctorChecksFailed
	}
	return nil
}

func appendCheck(r DoctorReport, name string, pass bool, detail string) DoctorReport {
	r.Checks = append(r.Checks, DoctorCheck{Name: name, Pass: pass, Detail: detail})
	if !pass {
		r.OverallPass = false
	}
	return r
}

func lastCheckPassed(r DoctorReport) bool {
	if len(r.Checks) == 0 {
		return true
	}
	return r.Checks[len(r.Checks)-1].Pass
}

func checkPGReachable(ctx context.Context, dsn string, opts transport.Options) (bool, string) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := transport.New(cctx, dsn, opts)
	if err != nil {
		return false, err.Error()
	}
	defer pool.Close()
	if err := pool.HealthCheck(cctx); err != nil {
		return false, err.Error()
	}
	return true, "ok"
}

func checkSchemaCurrent(ctx context.Context, dsn string, opts transport.Options) (bool, string) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	pool, err := transport.New(cctx, dsn, opts)
	if err != nil {
		return false, err.Error()
	}
	defer pool.Close()
	var n int
	err = pool.Inner().QueryRow(cctx,
		`SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename='sync_projects'`,
	).Scan(&n)
	if err != nil {
		return false, err.Error()
	}
	if n != 1 {
		return false, "sync_projects table missing — run 'mtix sync init'"
	}
	return true, "ok"
}

// checkQueueDraining flags pending events older than 1 hour as a
// stuck-queue indicator. The threshold is conservative: a healthy
// pusher drains within seconds.
func checkQueueDraining(ctx context.Context, store *sqlite.Store) (bool, string) {
	cutoff := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	var n int
	if err := store.QueryRow(ctx, `
		SELECT COUNT(*) FROM sync_events
		WHERE sync_status = 'pending' AND created_at < ?`, cutoff,
	).Scan(&n); err != nil {
		return false, err.Error()
	}
	if n > 0 {
		return false, fmt.Sprintf("%d events pending for >1h — pusher may be stuck", n)
	}
	return true, "ok"
}

// checkNoOrphanApplied flags applied_events rows whose target node
// doesn't exist locally. This catches a class of bugs where a node
// was deleted but its applied_events row survived.
func checkNoOrphanApplied(ctx context.Context, store *sqlite.Store) (bool, string) {
	var n int
	if err := store.QueryRow(ctx, `
		SELECT COUNT(*) FROM applied_events ae
		LEFT JOIN sync_events se ON se.event_id = ae.event_id
		LEFT JOIN nodes n ON n.id = se.node_id
		WHERE se.event_id IS NOT NULL AND n.id IS NULL
		  AND se.op_type != 'delete'`,
	).Scan(&n); err != nil {
		return false, err.Error()
	}
	if n > 0 {
		return false, fmt.Sprintf("%d orphan applied_events (event references missing node)", n)
	}
	return true, "ok"
}

// checkSecretsFileMode verifies .mtix/secrets is 0600 when present.
// File absence is fine (DSN may live in env var); only an actual
// permissions issue is a fail.
func checkSecretsFileMode(mtixDir string) (bool, string) {
	path := filepath.Join(mtixDir, transport.SecretsFilename)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return true, "secrets file absent (DSN via env var)"
	}
	if err != nil {
		return false, err.Error()
	}
	mode := info.Mode().Perm()
	if mode != transport.SecretsRequiredMode {
		return false, fmt.Sprintf("%s: %#o (want %#o)",
			path, mode, transport.SecretsRequiredMode)
	}
	return true, "ok"
}

func printDoctorTable(w io.Writer, r DoctorReport) {
	for _, c := range r.Checks {
		mark := "PASS"
		if !c.Pass {
			mark = "FAIL"
		}
		fmt.Fprintf(w, "[%s] %-20s %s\n", mark, c.Name, c.Detail)
	}
	fmt.Fprintln(w)
	if r.OverallPass {
		fmt.Fprintln(w, "all checks passed")
	} else {
		fmt.Fprintln(w, "one or more checks FAILED — see above")
	}
}
