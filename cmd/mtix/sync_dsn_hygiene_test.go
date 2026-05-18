// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/stretchr/testify/require"
)

// TestDSN_NeverInAnyFR18CommandOutput is the FR-18.17 / MTIX-15.7.5
// regression sweep. For each FR-18 sync command, run with a
// deliberately-leaking DSN containing redact.SecretSentinel and
// assert the sentinel does NOT appear in stdout or stderr.
//
// Commands run in error mode (no real PG behind them); the test
// exercises the "DSN flowed into an error message" code paths since
// those are the most likely leak vectors. The successful-execution
// paths require a live PG and are exercised by the integration tests
// when MTIX_PG_TEST_DSN is set.
//
// The sentinel string is defined in internal/sync/redact and is
// known across the sync packages; the same value used in 15.3.4's
// transport security tests.
func TestDSN_NeverInAnyFR18CommandOutput(t *testing.T) {
	saved := app.mtixDir
	dir := t.TempDir()
	app.mtixDir = dir
	t.Cleanup(func() { app.mtixDir = saved })

	leakyDSN := "postgres://user:" + redact.SecretSentinel + "@hub.example.com:5432/mtix"
	t.Setenv("MTIX_SYNC_DSN", leakyDSN)
	t.Setenv("MTIX_SYNC_HOOK", "")

	// Each invocation captures its own buffers and asserts the sentinel
	// is absent regardless of whether the command succeeded or errored.
	type cmdRunner func(ctx context.Context, stdout, stderr *bytes.Buffer)
	cases := []struct {
		name string
		run  cmdRunner
	}{
		{"init", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			_ = runSyncInit(ctx, stdout, stderr, nil, transport.Options{InsecureTLS: true})
		}},
		{"clone", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			_ = runSyncClone(ctx, stdout, stderr, nil, transport.Options{InsecureTLS: true}, false, 1000)
		}},
		{"push", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			_ = runSyncPush(ctx, stdout, stderr, nil, transport.Options{InsecureTLS: true}, true)
		}},
		{"pull", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			_ = runSyncPull(ctx, stdout, stderr, nil, transport.Options{InsecureTLS: true}, 1000)
		}},
		{"status", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			_ = runSyncStatus(ctx, stdout, stderr)
		}},
		{"doctor", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			_ = runSyncDoctor(ctx, stdout, stderr, nil, transport.Options{InsecureTLS: true})
		}},
		{"conflicts list", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			_ = runSyncConflictsList(ctx, stdout, stderr, "")
		}},
		{"conflicts resolve", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			_ = runSyncConflictsResolve(ctx, stdout, stderr, "1", "keep-local")
		}},
		{"reconcile", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			_ = runSyncReconcile(ctx, stdout, stderr, reconcileFlags{discardLocal: true})
		}},
		{"backup", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			_ = runSyncBackup(ctx, stdout, stderr, nil, "/tmp/__nonexistent_path_for_test")
		}},
		{"backfill", func(ctx context.Context, stdout, stderr *bytes.Buffer) {
			// Backfill is local-only; it does not open a PG connection and
			// thus cannot leak the DSN. Run it in the sweep anyway so a
			// future refactor that accidentally opens PG from this code
			// path is caught.
			_ = runSyncBackfill(ctx, stdout, stderr, true /*dryRun*/, false)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			tc.run(context.Background(), &stdout, &stderr)

			out := stdout.String() + stderr.String()
			require.NotContainsf(t, out, redact.SecretSentinel,
				"%s leaked DSN sentinel into observable output:\n%s",
				tc.name, out)
		})
	}
}

func TestDSN_RedactDSNCatchesAllSchemes(t *testing.T) {
	// Sanity that the redact package does what the sweep relies on.
	cases := []struct {
		scheme string
		dsn    string
	}{
		{"postgres://", "postgres://user:" + redact.SecretSentinel + "@host/db"},
		{"postgresql://", "postgresql://user:" + redact.SecretSentinel + "@host/db"},
		{"jdbc:postgresql://", "jdbc:postgresql://user:" + redact.SecretSentinel + "@host/db"},
	}
	for _, tc := range cases {
		t.Run(tc.scheme, func(t *testing.T) {
			redacted := redact.DSN(tc.dsn)
			require.NotContainsf(t, redacted, redact.SecretSentinel,
				"%s scheme leaked the sentinel after redaction", tc.scheme)
			// The redacted form should still contain the host so
			// operators can debug — verify via the host substring
			// derived from the DSN.
			require.True(t, strings.Contains(redacted, "host/db") || strings.Contains(redacted, "@host"),
				"redaction should preserve host: got %q", redacted)
		})
	}
}
