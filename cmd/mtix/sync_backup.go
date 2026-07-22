// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// pgDumpBin is the executable invoked by mtix sync backup. Override
// via env (MTIX_PG_DUMP) for tests / non-standard installs.
var pgDumpBin = func() string {
	if v := os.Getenv("MTIX_PG_DUMP"); v != "" {
		return v
	}
	return "pg_dump"
}

// backupTables are the mtix-owned tables included in the dump per
// FR-18.21. Excludes anything not under mtix's control on the hub
// (other applications sharing the PG instance, etc.).
var backupTables = []string{
	"sync_events",
	"sync_conflicts",
	"sync_projects",
	"applied_events",
	"audit_log",
}

// newSyncBackupCmd creates `mtix sync backup --output FILE` per
// FR-18.21. Wraps pg_dump for the mtix-owned tables. Restore is
// documented in workflows/safety-critical.md (lands in 15.12).
func newSyncBackupCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "backup [DSN]",
		Short: "Dump the mtix-owned hub tables to a portable SQL file (FR-18.21)",
		Long: `Invoke pg_dump to write a portable SQL dump of the mtix-owned
tables on the BYO Postgres hub: sync_events, sync_conflicts,
sync_projects, applied_events, audit_log.

The output file is suitable for psql restore via:
    psql "$DSN" < FILE

Requires pg_dump on PATH (override via MTIX_PG_DUMP env var). The
DSN must point at the hub; rotation/retention of the backup file is
the operator's responsibility.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncBackup(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, output)
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "Path to the output SQL file (required)")
	if err := cmd.MarkFlagRequired("output"); err != nil {
		panic(err)
	}
	return cmd
}

func runSyncBackup(ctx context.Context, stdout, stderr io.Writer,
	args []string, output string,
) error {
	if output == "" {
		return fmt.Errorf("mtix sync backup: --output is required")
	}
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync backup: not in an mtix project")
	}

	dsn, err := resolveSyncDSN(args)
	if err != nil {
		return wrapSyncErr(stderr, "dsn", err)
	}

	// Build the pg_dump argv: --table=... per backup table, plus
	// the connection string and -f for output.
	argv := []string{"--no-owner", "--no-privileges", "-f", output}
	for _, t := range backupTables {
		argv = append(argv, "--table="+t)
	}
	argv = append(argv, dsn)

	cmd := exec.CommandContext(ctx, pgDumpBin(), argv...) //nolint:gosec // pgDumpBin overridable for tests
	cmd.Stderr = stderr

	// MTIX-59: when the DSN asks pg_dump to verify the server cert but names no
	// trust root, libpq defaults to ~/.postgresql/root.crt and aborts when it is
	// absent — a routine failure for cloud hubs (Neon/Supabase DSNs are usually
	// verify-full). Fill that gap ONLY when nothing else is configured, defaulting
	// to the OS trust store so public-CA providers back up out of the box.
	if root := backupSSLRootCertEnv(dsn); root != "" {
		cmd.Env = append(os.Environ(), "PGSSLROOTCERT="+root)
		fmt.Fprintf(stderr, "mtix sync backup: DSN requests TLS verification but names no "+
			"sslrootcert and no ~/.postgresql/root.crt exists — using the system trust store "+
			"(PGSSLROOTCERT=system). A private-CA hub (e.g. Supabase) needs an explicit "+
			"sslrootcert=<ca.pem> in the DSN.\n")
	}

	if err := cmd.Run(); err != nil {
		// pg_dump's stderr already captured; surface a wrapped message
		// for the caller. Redact DSN in the wrapped form.
		return fmt.Errorf("mtix sync backup: pg_dump failed: %w", err)
	}

	fmt.Fprintf(stdout, "backup written to %s (tables: %s)\n",
		output, strings.Join(backupTables, ", "))
	return nil
}

// backupSSLRootCertEnv returns the PGSSLROOTCERT value pg_dump should use, or ""
// for no override. It defaults to "system" (the OS trust store) ONLY when the
// DSN requests certificate verification (sslmode=verify-ca/verify-full) yet the
// operator has configured no trust root at all — no sslrootcert in the DSN, no
// PGSSLROOTCERT in the environment, and no ~/.postgresql/root.crt on disk. In
// every other case it returns "" so an explicit operator choice is never
// overridden. "system" requires libpq >= 16, which any pg_dump new enough to
// dump a modern managed server already is (MTIX-59).
func backupSSLRootCertEnv(dsn string) string {
	low := strings.ToLower(dsn)
	if !strings.Contains(low, "sslmode=verify") {
		return "" // no verification requested → libpq needs no trust root
	}
	if strings.Contains(low, "sslrootcert=") {
		return "" // operator named a cert explicitly
	}
	if os.Getenv("PGSSLROOTCERT") != "" {
		return "" // operator configured one via the environment
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, statErr := os.Stat(filepath.Join(home, ".postgresql", "root.crt")); statErr == nil {
			return "" // libpq's default trust root exists; respect it
		}
	}
	return "system"
}
