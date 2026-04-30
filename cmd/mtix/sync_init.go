// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// newSyncInitCmd creates the `mtix sync init` command per FR-18 /
// MTIX-15.7.1. The first user runs this to migrate the hub schema
// and register the local project's first_event_hash.
//
// Usage:
//
//	mtix sync init <DSN>
//	mtix sync init  # reads DSN from MTIX_SYNC_DSN env var or .mtix/secrets
//
// Behavior:
//  1. Resolve the DSN via transport.Source (refuses tracked-config DSNs).
//  2. Open a TLS-verify-full pool against the hub.
//  3. Run the schema migration under PG advisory lock.
//  4. Compute the local first_event_hash if the local store has events.
//  5. Detect divergent history if the hub already has the prefix.
//  6. Otherwise, write the local first_event_hash to the hub's
//     sync_projects table (a v0.2 follow-up; for now the local cache
//     is set and the hub picks up first_event_hash from the first
//     successful PushEvents call).
//
// Hook-friendly: when MTIX_SYNC_HOOK=1 is set, transient PG errors
// exit 0 with a WARN line on stderr. Hard errors (DSN missing,
// validation, divergent history) still fail-closed.
func newSyncInitCmd() *cobra.Command {
	var insecureTLS bool

	cmd := &cobra.Command{
		Use:   "init [DSN]",
		Short: "Initialize the sync hub for this project (FR-18)",
		Long: `Initialize the BYO Postgres sync hub for this project. Runs the schema
migration under a PG advisory lock so concurrent first-connects are safe.

DSN sources (FR-18.16):
  1. Argument:           mtix sync init postgres://...
  2. Environment:        MTIX_SYNC_DSN=postgres://... mtix sync init
  3. Secrets file:       .mtix/secrets (mode 0600, gitignored)

The DSN is refused if found in any tracked .mtix/config.* file. The
default sslmode is verify-full; --insecure-tls is accepted only for
loopback hosts.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncInit(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS})
		},
	}

	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes on loopback hosts (development only)")
	return cmd
}

// runSyncInit executes the init flow. Extracted from the cobra
// closure so unit tests can call it directly with mock writers.
func runSyncInit(ctx context.Context, stdout, stderr io.Writer, args []string, opts transport.Options) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync init: not in an mtix project (run 'mtix init' first)")
	}

	dsn, err := resolveSyncDSN(args)
	if err != nil {
		return wrapSyncErr(stderr, "dsn", err)
	}

	connectCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pool, err := transport.New(connectCtx, dsn, opts)
	if err != nil {
		return wrapSyncErr(stderr, "connect", err)
	}
	defer pool.Close()

	if migrateErr := pool.Migrate(connectCtx); migrateErr != nil {
		return wrapSyncErr(stderr, "migrate", migrateErr)
	}

	if app.store == nil {
		// Mtix project not initialized — migration succeeded on the
		// hub, which is fine for a brand-new project; nothing local
		// to register.
		fmt.Fprintln(stdout,
			"hub schema migrated; no local store yet (run 'mtix init' to start)")
		return nil
	}

	prefix, hash, err := app.store.GetOrComputeLocalFirstEventHash(ctx)
	if err != nil {
		return wrapSyncErr(stderr, "local first_event_hash", err)
	}
	if prefix == "" {
		fmt.Fprintln(stdout,
			"hub schema migrated; no local events yet (your first mtix create will register the project)")
		return nil
	}

	hubPrefix, hubHash, err := readHubFirstEventHash(connectCtx, pool, prefix)
	if err != nil {
		return wrapSyncErr(stderr, "hub first_event_hash", err)
	}
	if err := sqlite.DetectDivergentHistory(prefix, hash, hubPrefix, hubHash); err != nil {
		return wrapSyncErr(stderr, "divergence", err)
	}

	fmt.Fprintf(stdout,
		"hub schema migrated; local project %s registered (first_event_hash=%s)\n",
		prefix, shortHashForCLI(hash))
	return nil
}

// readHubFirstEventHash queries hub.sync_projects for the prefix.
// Returns ("", "", nil) when the hub has no row yet (fresh hub for
// this project — caller can proceed without a divergence check).
func readHubFirstEventHash(ctx context.Context, pool *transport.Pool, prefix string) (string, string, error) {
	rows, err := pool.Inner().Query(ctx,
		`SELECT project_prefix, first_event_hash FROM sync_projects WHERE project_prefix = $1`, prefix)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", "", nil
	}
	var p, h string
	if err := rows.Scan(&p, &h); err != nil {
		return "", "", err
	}
	return p, h, rows.Err()
}

// resolveSyncDSN returns the DSN per FR-18.16: positional arg if
// supplied, else transport.Source which checks env + .mtix/secrets
// and refuses tracked-config DSNs.
func resolveSyncDSN(args []string) (string, error) {
	if len(args) == 1 && args[0] != "" {
		return args[0], nil
	}
	return transport.Source(app.mtixDir)
}

// wrapSyncErr formats CLI-side errors with consistent prefix +
// honors hook mode (MTIX_SYNC_HOOK=1) for transient errors per FR-18.19.
//
// All error messages flow through redact.DSN before any caller logs
// them so a malformed DSN can't leak credentials to stderr.
func wrapSyncErr(stderr io.Writer, stage string, err error) error {
	if isHookMode() && isTransientSyncErr(err) {
		fmt.Fprintf(stderr,
			"WARN: mtix sync %s degraded: %s (continuing per MTIX_SYNC_HOOK=1)\n",
			stage, redact.DSN(err.Error()))
		return nil
	}
	return fmt.Errorf("mtix sync %s: %s", stage, redact.DSN(err.Error()))
}

// isHookMode reports whether MTIX_SYNC_HOOK=1 is set per FR-18.19.
// In hook mode, transient PG errors must not block the surrounding
// git push.
func isHookMode() bool {
	return os.Getenv("MTIX_SYNC_HOOK") == "1"
}

// isTransientSyncErr is the CLI-layer mirror of transport.isTransient
// (which is package-private). Cheap heuristic: network errors,
// statement-timeout codes, and the common pgx connection errors all
// stringify with "connection", "timeout", or "network" — those are
// transient. Auth and validation errors are NOT.
func isTransientSyncErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, model.ErrSyncDivergentHistory) ||
		errors.Is(err, model.ErrInvalidInput) ||
		errors.Is(err, transport.ErrDSNNotConfigured) ||
		errors.Is(err, transport.ErrSecretsFileMode) ||
		errors.Is(err, transport.ErrDSNInTrackedFile) ||
		errors.Is(err, transport.ErrTLSWeakNonLoopback) ||
		errors.Is(err, transport.ErrTLSWeakWithoutFlag) {
		return false
	}
	msg := err.Error()
	for _, sub := range []string{"connection refused", "no route to host", "i/o timeout", "broken pipe"} {
		if containsCI(msg, sub) {
			return true
		}
	}
	return false
}

// containsCI is a case-insensitive substring check.
func containsCI(s, sub string) bool {
	return len(sub) <= len(s) && lowerContains(s, sub)
}

// lowerContains lower-cases both inputs and reports whether s
// contains sub as a substring.
func lowerContains(s, sub string) bool {
	ls := lowerASCII(s)
	lsub := lowerASCII(sub)
	for i := 0; i+len(lsub) <= len(ls); i++ {
		if ls[i:i+len(lsub)] == lsub {
			return true
		}
	}
	return false
}

func lowerASCII(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

// shortHashForCLI returns the first 12 chars of a hash for safe CLI display.
func shortHashForCLI(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}
