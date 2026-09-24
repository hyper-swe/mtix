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

// DoctorCheck is one row in the doctor's report. Warn marks a passing
// check that needs attention: it is shown as WARN and keeps the doctor's
// exit code 0 (MTIX-95.1). Fix is the command that addresses the check,
// and Findings carries the hub-privileges check's findings. The three are
// omitted when empty, so the other checks marshal as before.
type DoctorCheck struct {
	Name     string                       `json:"name"`
	Pass     bool                         `json:"pass"`
	Warn     bool                         `json:"warn,omitempty"`
	Detail   string                       `json:"detail,omitempty"`
	Fix      string                       `json:"fix,omitempty"`
	Findings []transport.PrivilegeFinding `json:"findings,omitempty"`
}

// DoctorReport aggregates the health checks per FR-18 / MTIX-15.7.3.
type DoctorReport struct {
	OverallPass bool          `json:"pass"`
	Checks      []DoctorCheck `json:"checks"`
}

// errDoctorChecksFailed is returned by runSyncDoctor when one or more
// health checks fail. The cobra wrapper translates this into exit
// code 2 (distinguishing failed-checks from invalid-arguments). Tests
// observe the sentinel without the process being killed.
var errDoctorChecksFailed = errors.New("doctor checks failed")

// syncDoctorLong is the help text of `mtix sync doctor`: every check, the
// connect budget and when a check warns or fails (FR-18, MTIX-95.1,
// MTIX-95.7).
const syncDoctorLong = `Run health checks against the local store and the BYO Postgres hub:

  PG reachable           - opens pool + Ping
  Schema current         - sync_projects table exists with expected columns
  Queue draining         - no events older than 1h still in pending
  No orphan applied      - every applied_event has a matching node OR tombstone
  DSN secrets file mode  - .mtix/secrets is mode 0600 (when present)
  Hub triggers           - every function and trigger the hub migrations
                           define exists, and every trigger is enabled
                           (tgenabled 'O')
  Hub privileges         - which roles other than the table owner can use the
                           sync tables, and whether every TRUNCATE guard is in
                           place (the check mtix sync harden runs)

Each hub check allows 30 s to connect, the same budget as mtix sync init,
clone, push and pull, so a hub that is resuming from idle passes.

Hub triggers names each missing function or trigger and each trigger that
is not enabled, with the fix, run as the table owner: mtix sync init for
what is missing, and the ALTER TABLE ... ENABLE TRIGGER statement it
prints for what is not enabled. Like hub privileges, it is a WARN by
default and fails in strict mode.

Hub privileges is a WARN by default: roles other than the owner may use
the sync tables, which can be fine when the database is reachable only
from a private network; mtix sync harden restricts them. It fails only in
strict mode, when the sync.keep_roles config key is set: then it fails
whenever mtix sync harden would report a finding, not only a role outside
the list or a missing or disabled TRUNCATE guard, but also a kept role
that can grant its access on or holds a privilege another role granted
it, an mtix object owned by another role, and a membership through which
a role can reach every table. If the check cannot run, it is a WARN by
default and fails in strict mode. The check contacts the hub only while
the doctor runs.

Exit code: 0 on all-pass, including checks that pass with a WARN; 2 if
any check fails. --json output for agents and CI consumption.`

// newSyncDoctorCmd creates `mtix sync doctor`. Exits 0 if every check
// passes, exits 2 if any fails. --json output for machine consumption.
func newSyncDoctorCmd() *cobra.Command {
	var insecureTLS bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Run sync health checks (FR-18)",
		Long:  syncDoctorLong,
		Args:  syncExactArgs(0),
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
		"Allow weaker TLS modes only when every host the connection may use is loopback or a local socket (development only)")
	return cmd
}

// runSyncDoctor runs every health check and prints the report
// (FR-18, MTIX-15.7.3). The hub checks connect within syncConnectBudget,
// the budget sync itself uses (MTIX-95.7). Returns errDoctorChecksFailed
// when a check fails.
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

	// Checks 1 and 2: PG reachable, schema current.
	report, hubReady := appendHubReadyChecks(ctx, report, dsn, dsnErr, opts)

	// Checks 3 to 5: queue draining, no orphan applied, secrets file mode.
	report = appendLocalChecks(ctx, report)

	// Check 6: every mtix function and trigger the migrations define is
	// on the hub and enabled, as the restore runbook ends (MTIX-95.7).
	report = appendDoctorCheck(report, checkHubObjects(ctx, dsn, hubReady, opts))

	// Check 7: hub privileges, the verification mtix sync harden runs (MTIX-95.1).
	report = appendDoctorCheck(report, checkHubPrivileges(ctx, dsn, hubReady, opts))

	// No detail may carry the DSN or its password (FR-18.17, MTIX-95.15).
	report = scrubDoctorReport(report)

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

// appendHubReadyChecks adds the PG reachable and schema current checks
// and reports whether both passed, so the checks that read the hub's
// catalog can run.
func appendHubReadyChecks(ctx context.Context, report DoctorReport, dsn string, dsnErr error,
	opts transport.Options,
) (DoctorReport, bool) {
	if dsnErr != nil {
		report = appendCheck(report, "PG reachable", false, "DSN: "+dsnErr.Error())
	} else {
		pgOK, detail := checkPGReachable(ctx, dsn, opts)
		report = appendCheck(report, "PG reachable", pgOK, detail)
	}

	// Schema current is only meaningful if PG is reachable.
	if dsnErr == nil && lastCheckPassed(report) {
		schemaOK, detail := checkSchemaCurrent(ctx, dsn, opts)
		report = appendCheck(report, "schema current", schemaOK, detail)
	} else {
		report = appendCheck(report, "schema current", false, "skipped (PG unreachable)")
	}
	return report, dsnErr == nil && lastCheckPassed(report)
}

// appendLocalChecks adds the checks that read only the local store and
// the project directory.
func appendLocalChecks(ctx context.Context, report DoctorReport) DoctorReport {
	if app.store == nil {
		report = appendCheck(report, "queue draining", false, "local store not initialized")
		report = appendCheck(report, "no orphan applied", false, "local store not initialized")
	} else {
		drainOK, detail := checkQueueDraining(ctx, app.store)
		report = appendCheck(report, "queue draining", drainOK, detail)
		orphanOK, detail := checkNoOrphanApplied(ctx, app.store)
		report = appendCheck(report, "no orphan applied", orphanOK, detail)
	}
	modeOK, detail := checkSecretsFileMode(app.mtixDir)
	return appendCheck(report, "secrets file mode", modeOK, detail)
}

func appendCheck(r DoctorReport, name string, pass bool, detail string) DoctorReport {
	return appendDoctorCheck(r, DoctorCheck{Name: name, Pass: pass, Detail: detail})
}

// appendDoctorCheck adds c to r. A failing check fails the report; a WARN,
// which passes, does not (MTIX-95.1).
func appendDoctorCheck(r DoctorReport, c DoctorCheck) DoctorReport {
	r.Checks = append(r.Checks, c)
	if !c.Pass {
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

// checkPGReachable opens a pool and pings the hub within
// syncConnectBudget, the same connect budget as sync init, clone, push
// and pull, so a hub resuming from idle passes whenever it would sync
// (MTIX-95.7).
func checkPGReachable(ctx context.Context, dsn string, opts transport.Options) (bool, string) {
	cctx, cancel := context.WithTimeout(ctx, syncConnectBudget)
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

// checkSchemaCurrent checks, within syncConnectBudget, that the hub has
// the sync_projects table (MTIX-95.7).
func checkSchemaCurrent(ctx context.Context, dsn string, opts transport.Options) (bool, string) {
	cctx, cancel := context.WithTimeout(ctx, syncConnectBudget)
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

// printDoctorTable writes one line per check, marked PASS, WARN or FAIL,
// and a summary that counts warnings (MTIX-95.1).
func printDoctorTable(w io.Writer, r DoctorReport) {
	warnings := 0
	for _, c := range r.Checks {
		mark := "PASS"
		switch {
		case !c.Pass:
			mark = "FAIL"
		case c.Warn:
			mark = "WARN"
			warnings++
		}
		fmt.Fprintf(w, "[%s] %-20s %s\n", mark, c.Name, c.Detail)
	}
	fmt.Fprintln(w)
	switch {
	case !r.OverallPass:
		fmt.Fprintln(w, "one or more checks FAILED \u2014 see above")
	case warnings == 1:
		fmt.Fprintln(w, "all checks passed (1 warning)")
	case warnings > 1:
		fmt.Fprintf(w, "all checks passed (%d warnings)\n", warnings)
	default:
		fmt.Fprintln(w, "all checks passed")
	}
}
