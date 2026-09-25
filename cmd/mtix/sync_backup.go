// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// pgDumpBin is the executable invoked by mtix sync backup. Override
// via env (MTIX_PG_DUMP) for tests / non-standard installs.
var pgDumpBin = func() string {
	if v := os.Getenv("MTIX_PG_DUMP"); v != "" {
		return v
	}
	return "pg_dump"
}

// pgDumpNoTableMessage starts pg_dump's error for a --table name that
// matches no table: `no matching tables were found for pattern "<name>"`
// under --strict-names, or `no matching tables were found` when none
// matches (MTIX-95.7.4).
const pgDumpNoTableMessage = "no matching tables were found"

// errHubTableNotFound marks a backup that pg_dump stopped because a hub
// table name matched no table through the search_path of the role it
// connects as (--strict-names, MTIX-95.7.4).
var errHubTableNotFound = errors.New("a hub table was not found (pg_dump names it above)")

// pgSystemTrustStore is the sslrootcert value that makes libpq (16 and
// later) verify the server against the operating system's trust store,
// as the sync transport does when no CA is configured (MTIX-59).
const pgSystemTrustStore = "system"

// syncBackupLong is the help of mtix sync backup: what it dumps, how it
// connects (including the search_path and schema-usage steps for the DSN's
// role), the output file and the restore runbook (FR-18.21, MTIX-95.7,
// MTIX-95.7.4, MTIX-95.1.4).
const syncBackupLong = `Invoke pg_dump to write a portable SQL dump of every table the mtix hub
migrations create, with its data; the report lists the tables. Every one
of them must exist: a hub that lacks one, such as a hub not initialized
since an upgrade added a table, fails the backup with a hint to run mtix
sync init. pg_dump's own messages are shown untranslated (it runs with
LC_MESSAGES=C), with the DSN's password removed.

The connection uses the TLS settings the sync commands use: sslmode is
verify-full when the DSN names none, and a weaker sslmode needs
--insecure-tls and is allowed only when every host is loopback or a local
socket. pg_dump receives every host and port, the CA file (sslrootcert in
the DSN, or MTIX_SYNC_SSLROOTCERT) and target_session_attrs through PG*
environment variables; the DSN and its password are never on its command
line. pg_dump does not receive the DSN's options, so it finds the tables
through the default search_path of the role the DSN names, which may not
be the table owner: for a hub whose schema is named only in the DSN, first
run ALTER ROLE <the DSN's role> IN DATABASE <the DSN's database> SET
search_path = <schema>, public. It applies in that database only and
takes precedence over a role-wide ALTER ROLE <the DSN's role> SET
search_path = <schema>, public. If the role the DSN names lacks USAGE on
the hub's schema, pg_dump does not see the tables either: the failed
backup prints the GRANT statements, naming each sync table and sequence,
that the table owner runs. Client certificates (sslcert, sslkey) are not
passed to pg_dump, so a hub that requires one cannot be backed up with
this command yet.

mtix creates the output file, readable and writable only by you (mode
0600), before pg_dump writes to it. An existing file is never overwritten:
choose a new path for each backup. A failed backup, or one interrupted
with Ctrl-C or SIGTERM, leaves no file.

The dump holds the tables and their data, not the mtix functions and
triggers. To restore into an empty database:
  1. psql -f FILE, connected as the role that will own the sync tables,
     with PGSSLROOTCERT naming the hub's CA file (or system, with libpq 16
     or later); psql reports errors for the triggers, whose functions do
     not exist yet, and step 2 creates them
  2. mtix sync init, with the DSN naming that role
  3. mtix sync doctor: its hub-triggers check passes
  4. mtix sync mark-restored

Requires pg_dump on PATH (override via MTIX_PG_DUMP env var). Rotation
and retention of the backup file are the operator's responsibility.`

// newSyncBackupCmd creates `mtix sync backup --output FILE` per
// FR-18.21. Wraps pg_dump for every hub table the migrations create.
// The restore runbook is in its help and the user manual (MTIX-95.7).
func newSyncBackupCmd() *cobra.Command {
	var (
		output      string
		insecureTLS bool
	)
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Dump the mtix-owned hub tables to a portable SQL file (FR-18.21)",
		Long:  syncBackupLong,
		Args:  syncExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncBackup(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, output, transport.Options{InsecureTLS: insecureTLS})
		},
	}
	cmd.Flags().StringVar(&output, "output", "", "Path to the output SQL file (required)")
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes only when every host the connection may use is loopback or a local socket (development only)")
	if err := cmd.MarkFlagRequired("output"); err != nil {
		panic(err)
	}
	return cmd
}

// runSyncBackup dumps every hub table to output (FR-18.21, MTIX-95.7):
// the table list comes from migrations.Tables(), pg_dump connects with the
// settings the sync transport approves (pgDumpConnParams), and the output
// file is created 0600 and exclusive before pg_dump writes to it. A failed
// backup, one interrupted by SIGINT or SIGTERM included, removes the file
// it created. A backup that failed because a hub table was not found also
// gives the search_path step for the DSN's role in the DSN's database
// (withSearchPathAdvice) and the schema-usage grants for that role, by
// table and sequence name (withSchemaUsageAdvice), and the success message
// lists the tables, every one of which pg_dump found (MTIX-95.7.4,
// MTIX-95.1.4).
func runSyncBackup(ctx context.Context, stdout, stderr io.Writer,
	args []string, output string, opts transport.Options,
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
	conn, err := pgDumpConnParams(dsn, opts)
	if err != nil {
		return wrapSyncErr(stderr, "dsn", err)
	}

	// Every table the hub migrations create: a table a new migration adds
	// is backed up with no list to update (D18).
	tables, err := migrations.Tables()
	if err != nil {
		return fmt.Errorf("mtix sync backup: hub tables: %w", err)
	}
	sequences, err := migrations.Sequences()
	if err != nil {
		return fmt.Errorf("mtix sync backup: hub sequences: %w", err)
	}

	// From here on SIGINT and SIGTERM cancel ctx instead of ending the
	// process, so an interrupted backup stops pg_dump and removes the
	// partial dump like any other failed backup.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	out, err := createBackupFile(output)
	if err != nil {
		return err
	}
	noteSystemTrustStore(stderr, conn)
	if err := dumpInto(ctx, out, stderr, conn, tables); err != nil {
		if ctx.Err() != nil {
			err = fmt.Errorf("mtix sync backup: interrupted, so the partial dump is removed: %w", ctx.Err())
		}
		// pg_dump connects as the DSN's role, with that role's default
		// search_path in the DSN's database: the steps for a hub whose
		// schema only the DSN names, or that the role may not use.
		err = withSearchPathAdvice(err, conn.user, conn.database)
		return discardBackup(out, output, withSchemaUsageAdvice(err, conn.user, tables, sequences))
	}
	if err := closeBackupFile(out); err != nil {
		return discardBackup(nil, output, err)
	}

	// pg_dump ran with --strict-names, so it found every listed table.
	fmt.Fprintf(stdout, "backup written to %s (tables: %s)\n", output, strings.Join(tables, ", "))
	return nil
}

// noteSystemTrustStore tells the operator, on stderr, when pg_dump will
// verify the hub against the system trust store because no CA file is
// configured (MTIX-59).
func noteSystemTrustStore(stderr io.Writer, conn pgDumpConn) {
	if conn.sslrootcert == pgSystemTrustStore {
		fmt.Fprintln(stderr, "mtix sync backup: no CA file is configured, so pg_dump verifies the hub's "+
			"certificate against the system trust store (PGSSLROOTCERT=system). If the hub's certificate "+
			"comes from a private CA, name it with sslrootcert=<ca.pem> in the DSN or with MTIX_SYNC_SSLROOTCERT.")
	}
}

// closeBackupFile flushes the finished dump to disk and closes it. On an
// error the file is closed, and the caller removes it (MTIX-95.7).
func closeBackupFile(out *os.File) error {
	if err := out.Sync(); err != nil {
		closeErr := out.Close()
		return errors.Join(fmt.Errorf("mtix sync backup: sync %s: %w", out.Name(), err), closeErr)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("mtix sync backup: close %s: %w", out.Name(), err)
	}
	return nil
}

// createBackupFile creates path for the dump, mode 0600 and exclusive
// (O_EXCL): the file is readable only by its owner from its first byte,
// and an existing file, or a symlink at path, is refused rather than
// overwritten or followed (MTIX-95.7).
func createBackupFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: the operator chooses the output path; O_EXCL refuses an existing file or symlink
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("mtix sync backup: %s: %w; backup never overwrites a file, choose a new path",
			path, model.ErrAlreadyExists)
	}
	if err != nil {
		return nil, fmt.Errorf("mtix sync backup: create %s: %w", path, err)
	}
	return f, nil
}

// dumpInto runs pg_dump for tables with conn's settings, writing the
// plain SQL dump to out (pg_dump's standard output) and its messages to
// stderr through the DSN scrubber (FR-18.17, MTIX-95.15). --strict-names
// makes every table name match a table, so a hub that lacks one fails the
// backup instead of producing a dump without it; that failure wraps
// errHubTableNotFound and says to run mtix sync init (MTIX-95.7.4).
func dumpInto(ctx context.Context, out *os.File, stderr io.Writer, conn pgDumpConn, tables []string) error {
	argv := []string{"--no-owner", "--no-privileges", "--strict-names"}
	for _, t := range tables {
		argv = append(argv, "--table="+t)
	}
	cmd := exec.CommandContext(ctx, pgDumpBin(), argv...) //nolint:gosec // G204: pgDumpBin is overridable for tests; arguments are fixed flags and migration table names
	notFound := &lineWatch{needle: []byte(pgDumpNoTableMessage)}
	pgStderr := newScrubWriter(io.MultiWriter(notFound, stderr))
	cmd.Stdout = out
	cmd.Stderr = pgStderr
	cmd.Env = conn.pgEnv(os.Environ())

	runErr := cmd.Run()
	if err := pgStderr.Flush(); err != nil {
		return fmt.Errorf("mtix sync backup: %w", err)
	}
	if runErr != nil && notFound.seen {
		return fmt.Errorf("mtix sync backup: pg_dump failed: %w: %w; if the hub has not been initialized since "+
			"an upgrade added the table, run mtix sync init, with the DSN naming the table owner, then back up again",
			errHubTableNotFound, runErr)
	}
	if runErr != nil {
		return fmt.Errorf("mtix sync backup: pg_dump failed: %w", runErr)
	}
	return nil
}

// lineWatch records whether a line written to it contains needle; it
// receives pg_dump's messages one scrubbed line per write (MTIX-95.7.4).
type lineWatch struct {
	needle []byte
	seen   bool
}

// Write notes whether p contains the needle and never fails.
func (w *lineWatch) Write(p []byte) (int, error) {
	if bytes.Contains(p, w.needle) {
		w.seen = true
	}
	return len(p), nil
}

// withSearchPathAdvice returns err with the search_path step added when it
// wraps errHubTableNotFound, and err unchanged otherwise. pg_dump receives
// no DSN options, so it resolves the tables through the default
// search_path of role in database: a hub whose schema is named only in
// the DSN is not visible to it until that search_path names the schema.
// role is the role pg_dump connects as: the DSN's user, or the OS user the
// driver uses when the DSN names none; database is the DSN's database. The
// step sets the search_path for role in database only, which takes
// precedence over the role-wide setting it also names. Both names are
// quoted as identifiers, so the statements run as printed; a placeholder
// stands for a name only when it is not known (MTIX-95.7.4).
func withSearchPathAdvice(err error, role, database string) error {
	if !errors.Is(err, errHubTableNotFound) {
		return err
	}
	roleName := quotedIdentOr(role, "<the DSN's role>")
	dbName := quotedIdentOr(database, "<the DSN's database>")
	return fmt.Errorf("%w; if the hub's schema is named only in the DSN, pg_dump, which connects as the DSN's role "+
		"without the DSN's options, does not see it: run ALTER ROLE %s IN DATABASE %s SET search_path = <schema>, public "+
		"(this database only; it takes precedence over a role-wide ALTER ROLE %s SET search_path = <schema>, public), "+
		"then back up again", err, roleName, dbName, roleName)
}

// withSchemaUsageAdvice returns err with the schema-usage step added when
// it wraps errHubTableNotFound, and err unchanged otherwise. A role without
// USAGE on a schema does not have it on its search_path, so pg_dump,
// connecting as role, finds no hub table there. The step grants role USAGE
// on the schema and SELECT on each of tables and sequences by name,
// qualified with a <schema> placeholder, so the table owner grants no more
// than the backup reads. role is quoted as an identifier, and a
// placeholder stands for it only when it is not known (MTIX-95.1.4).
func withSchemaUsageAdvice(err error, role string, tables, sequences []string) error {
	if !errors.Is(err, errHubTableNotFound) {
		return err
	}
	roleName := quotedIdentOr(role, "<the DSN's role>")
	grants := []string{"GRANT USAGE ON SCHEMA <schema> TO " + roleName}
	for _, g := range []struct {
		kind  string
		names []string
	}{{"TABLE", tables}, {"SEQUENCE", sequences}} {
		if len(g.names) > 0 {
			grants = append(grants, "GRANT SELECT ON "+g.kind+" <schema>."+
				strings.Join(g.names, ", <schema>.")+" TO "+roleName)
		}
	}
	return fmt.Errorf("%w; if the DSN's role lacks USAGE on the hub's schema, which leaves the schema off its "+
		"search_path, the table owner runs %s; then back up again (keep that role with mtix sync harden "+
		"--keep-role, so that harden leaves its access)", err, strings.Join(grants, "; "))
}

// quotedIdentOr returns name quoted as an SQL identifier, or placeholder
// when name is empty (MTIX-95.1.4).
func quotedIdentOr(name, placeholder string) string {
	if name == "" {
		return placeholder
	}
	return pgx.Identifier{name}.Sanitize()
}

// discardBackup closes f (when still open) and removes path, the output
// file of a failed backup, so no partial dump is left behind. It returns
// cause, joined with any error from the cleanup (MTIX-95.7).
func discardBackup(f *os.File, path string, cause error) error {
	errs := []error{cause}
	if f != nil {
		if err := f.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", path, err))
		}
	}
	if err := os.Remove(path); err != nil {
		errs = append(errs, fmt.Errorf("remove the partial dump %s: %w", path, err))
	}
	return errors.Join(errs...)
}

// pgDumpConn is the connection handed to pg_dump through PG* environment
// variables instead of a DSN string (MTIX-61). host and port are libpq's
// comma-separated lists.
type pgDumpConn struct {
	host, port, user, password, database     string
	sslmode, sslrootcert, targetSessionAttrs string
}

// pgDumpConnParams returns pg_dump's connection from the configuration
// the sync transport approves for dsn (transport.ApproveDSN, FR-18.15),
// so the backup connects with the TLS settings sync connects with: sslmode
// verify-full when the DSN names none, a weaker sslmode only with
// --insecure-tls and only when every host is loopback or a local socket,
// every host and port in dial order, the CA file and
// target_session_attrs. The DSN is not parsed a second time. When the
// connection verifies certificates and no CA is configured anywhere,
// pg_dump uses the system trust store, as the transport does (MTIX-59,
// MTIX-95.7).
func pgDumpConnParams(dsn string, opts transport.Options) (pgDumpConn, error) {
	approval, err := transport.ApproveDSN(dsn, opts)
	if err != nil {
		// ApproveDSN's errors name no DSN text (FR-18.17).
		return pgDumpConn{}, fmt.Errorf("backup connection: %w", err)
	}
	cc := &approval.Config.ConnConfig.Config
	hosts, ports := libpqHostList(cc)
	c := pgDumpConn{
		host: hosts, port: ports, user: cc.User, password: cc.Password, database: cc.Database,
		sslmode: approval.SSLMode, sslrootcert: approval.SSLRootCert, targetSessionAttrs: approval.TargetSessionAttrs,
	}
	if c.sslrootcert == "" {
		c.sslrootcert = backupSSLRootCertEnv(approval.SSLMode)
	}
	return c, nil
}

// libpqHostList returns the hosts and ports of cfg in dial order as
// libpq's comma-separated PGHOST and PGPORT lists: the primary host, then
// each fallback, with consecutive attempts at the same host and port (the
// driver tries a host twice under allow and prefer) listed once
// (MTIX-95.7).
func libpqHostList(cfg *pgconn.Config) (hosts, ports string) {
	type hostPort struct {
		host string
		port uint16
	}
	all := []hostPort{{cfg.Host, cfg.Port}}
	for _, f := range cfg.Fallbacks {
		all = append(all, hostPort{f.Host, f.Port})
	}
	var hs, ps []string
	for i, e := range all {
		if i > 0 && e == all[i-1] {
			continue
		}
		hs = append(hs, e.host)
		ps = append(ps, strconv.Itoa(int(e.port)))
	}
	return strings.Join(hs, ","), strings.Join(ps, ",")
}

// pgEnv returns base extended with the PG* variables pg_dump reads for its
// connection (MTIX-61). Empty fields are omitted, so pg_dump reads the
// same inherited variable the sync transport read. The approved settings
// are the only connection settings pg_dump applies: base keeps no
// connection service (PGSERVICE, PGSERVICEFILE), whose settings libpq
// applies over the environment, and no PGHOSTADDR, which the sync
// transport does not read (MTIX-95.7). pg_dump's messages are the
// untranslated ones, which dumpInto reads: LC_MESSAGES is C, and base's
// LC_ALL and LANGUAGE, which would override it, are dropped (MTIX-95.7.4).
func (c pgDumpConn) pgEnv(base []string) []string {
	env := make([]string, 0, len(base)+9)
	for _, kv := range base {
		switch name, _, _ := strings.Cut(kv, "="); name {
		case "PGSERVICE", "PGSERVICEFILE", "PGHOSTADDR", "LC_ALL", "LANGUAGE", "LC_MESSAGES":
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "LC_MESSAGES=C")
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
	add("PGTARGETSESSIONATTRS", c.targetSessionAttrs)
	return env
}

// backupSSLRootCertEnv returns the PGSSLROOTCERT value pg_dump should use
// when the approved configuration names no CA file, or "" for no override.
// It is the system trust store ONLY when sslmode verifies certificates
// (verify-ca or verify-full) and no trust root is configured at all: no
// PGSSLROOTCERT in the environment and no ~/.postgresql/root.crt. That is
// the trust the sync transport uses in the same case. "system" requires
// libpq 16 or later, which any pg_dump new enough to dump a modern server
// already is (MTIX-59, MTIX-95.7).
func backupSSLRootCertEnv(sslmode string) string {
	if sslmode != "verify-full" && sslmode != "verify-ca" {
		return "" // no verification requested → libpq needs no trust root
	}
	if os.Getenv("PGSSLROOTCERT") != "" {
		return "" // operator configured one via the environment
	}
	if home, err := os.UserHomeDir(); err == nil {
		if _, statErr := os.Stat(filepath.Join(home, ".postgresql", "root.crt")); statErr == nil {
			return "" // libpq's default trust root exists; respect it
		}
	}
	return pgSystemTrustStore
}
