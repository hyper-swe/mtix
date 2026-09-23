// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// scrubSyncText is the central scrubber for sync command output
// (FR-18.17, MTIX-95.15). Every sync error print, every doctor detail
// and the CLI's final error line pass through it. It removes the hub
// DSN this process can resolve, exactly and with every password it
// carries, then masks any URL-shaped DSN (redact.Known). It never
// parses the DSN, so a DSN that does not parse is removed too.
func scrubSyncText(s string) string {
	return redact.Known(s, knownSyncDSNs()...)
}

// knownSyncDSNs returns the hub DSN values this process can resolve
// (FR-18.16, MTIX-95.15): the MTIX_SYNC_DSN value and the content of
// .mtix/secrets. Both are returned when both are set, so the scrub does
// not depend on which one transport.Source would pick, on the secrets
// file's mode, or on the DSN being valid. An absent or unreadable
// secrets file holds no value to scrub and contributes nothing.
func knownSyncDSNs() []string {
	var dsns []string
	if v := os.Getenv(transport.EnvDSN); v != "" {
		dsns = append(dsns, v)
	}
	if app.mtixDir == "" {
		return dsns
	}
	path := filepath.Join(app.mtixDir, transport.SecretsFilename)
	if body, err := os.ReadFile(path); err == nil { //nolint:gosec // path is mtixDir + the fixed secrets filename
		dsns = append(dsns, string(body))
	}
	return dsns
}

// scrubDoctorReport returns r with every check detail passed through
// scrubSyncText, so neither the text nor the --json report of mtix sync
// doctor carries a DSN or its password (FR-18.17, MTIX-95.15).
func scrubDoctorReport(r DoctorReport) DoctorReport {
	checks := make([]DoctorCheck, len(r.Checks))
	for i, c := range r.Checks {
		c.Detail = scrubSyncText(c.Detail)
		checks[i] = c
	}
	r.Checks = checks
	return r
}

// syncExactArgs is the positional-argument rule for a sync command whose
// only positional arguments are the n it documents (FR-18.16,
// MTIX-95.15). An extra argument is refused with
// transport.ErrPositionalDSN, the same fixed refusal resolveSyncDSN
// gives. Unlike cobra.NoArgs and cobra.ExactArgs, neither refusal
// repeats an argument, which may be a DSN typed in the wrong place.
func syncExactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) > n {
			return fmt.Errorf("%s: %w", cmd.CommandPath(), transport.ErrPositionalDSN)
		}
		if len(args) < n {
			return fmt.Errorf("%s: requires %d argument(s), received %d", cmd.CommandPath(), n, len(args))
		}
		return nil
	}
}
