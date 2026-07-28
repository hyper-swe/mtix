// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
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

	// MTIX-61: parse the DSN with pgx (which tolerates DSN forms pg_dump's libpq
	// URI parser rejects — e.g. an unencoded special char in the password) and
	// hand pg_dump the connection via PG* env vars, invoking it with NO DSN arg.
	// Env values are literal, so there is no parsing/quoting ambiguity for any
	// password. Passing the raw DSN broke backup of a cloud hub whose password
	// contained an '@'.
	conn, err := pgDumpConnParams(dsn)
	if err != nil {
		return wrapSyncErr(stderr, "dsn", err)
	}

	argv := []string{"--no-owner", "--no-privileges", "-f", output}
	for _, t := range backupTables {
		argv = append(argv, "--table="+t)
	}

	cmd := exec.CommandContext(ctx, pgDumpBin(), argv...) //nolint:gosec // pgDumpBin overridable for tests
	cmd.Stderr = stderr
	cmd.Env = conn.pgEnv(os.Environ())
	if conn.sslrootcert == "system" {
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

// pgDumpConn is the connection, decomposed so it can be handed to pg_dump via
// PG* env vars instead of a DSN string (MTIX-61).
type pgDumpConn struct {
	host, port, user, password, database string
	sslmode, sslrootcert                 string
}

// pgDumpConnParams parses a Postgres DSN into discrete connection parameters.
// It uses pgconn.ParseConfig (the same lenient parser the sync transport uses,
// which accepts DSNs pg_dump's libpq URI parser rejects) for the credential
// fields, and reads sslmode/sslrootcert from the query string (everything after
// the first '?', so the password — which may contain URL-breaking characters —
// is never in the parsed span). When verification is requested but no trust
// root is configured, sslrootcert defaults to "system" (MTIX-59).
func pgDumpConnParams(dsn string) (pgDumpConn, error) {
	// Read ssl params from the query string ourselves, and strip it before
	// pgconn.ParseConfig — otherwise pgconn eagerly loads the sslrootcert file
	// (and applies TLS), which we neither need nor want here. Splitting at the
	// first '?' keeps the password span (before '?') untouched.
	var c pgDumpConn
	credDSN := dsn
	if i := strings.IndexByte(dsn, '?'); i >= 0 {
		credDSN = dsn[:i]
		if q, perr := url.ParseQuery(dsn[i+1:]); perr == nil {
			c.sslmode = q.Get("sslmode")
			c.sslrootcert = q.Get("sslrootcert")
		}
	}
	cfg, err := pgconn.ParseConfig(credDSN)
	if err != nil {
		return pgDumpConn{}, fmt.Errorf("parse backup dsn: %w", err)
	}
	c.host = cfg.Host
	c.port = strconv.Itoa(int(cfg.Port))
	c.user = cfg.User
	c.password = cfg.Password
	c.database = cfg.Database
	if c.sslrootcert == "" {
		c.sslrootcert = backupSSLRootCertEnv(dsn) // "system" default per MTIX-59, else ""
	}
	return c, nil
}

// pgEnv returns base extended with the PG* variables pg_dump reads for its
// connection (MTIX-61). Empty fields are omitted so pg_dump falls back to its
// own defaults.
func (c pgDumpConn) pgEnv(base []string) []string {
	env := append([]string{}, base...)
	add := func(k, v string) {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	add("PGHOST", c.host)
	add("PGPORT", c.port)
	add("PGUSER", c.user)
	add("PGPASSWORD", c.password)
	add("PGDATABASE", c.database)
	add("PGSSLMODE", c.sslmode)
	add("PGSSLROOTCERT", c.sslrootcert)
	return env
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
