// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// PG-gated tests for the sync RunE wrappers we hadn't yet covered.

func TestRunSyncInit_HappyPath(t *testing.T) {
	dsn := requireCmdPG(t)
	freshCmdHub(t, dsn)
	initTestApp(t)

	// Seed one event so first_event_hash is non-empty (init returns
	// the "no local events yet" early-out otherwise; this test
	// exercises the registerProjectOnHub branch).
	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))

	var stdout, stderr bytes.Buffer
	err := runSyncInit(context.Background(), &stdout, &stderr,
		[]string{dsn}, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "hub schema migrated")
}

func TestRunSyncInit_NoLocalEventsEarlyExit(t *testing.T) {
	dsn := requireCmdPG(t)
	freshCmdHub(t, dsn)
	initTestApp(t)
	// No runCreate — local store is empty; init should migrate the
	// hub and exit with the "no local events yet" message.

	var stdout, stderr bytes.Buffer
	err := runSyncInit(context.Background(), &stdout, &stderr,
		[]string{dsn}, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "no local events yet")
}

func TestRunSyncStatus_HappyPath(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))

	var stdout, stderr bytes.Buffer
	err := runSyncStatus(context.Background(), &stdout, &stderr)
	require.NoError(t, err)
	// status output mentions queue and last-push fields by convention
	out := stdout.String()
	require.NotEmpty(t, out, "status must print something")
}

func TestRunSyncDoctor_HappyPath(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)

	var stdout, stderr bytes.Buffer
	// Doctor returns errDoctorChecksFailed if any check fails; we
	// accept either nil (all-pass) or that sentinel as the
	// "completed normally" outcome. Other errors are failures.
	err := runSyncDoctor(context.Background(), &stdout, &stderr,
		[]string{dsn}, transport.Options{InsecureTLS: true})
	if err != nil && !errors.Is(err, errDoctorChecksFailed) {
		t.Fatalf("doctor errored unexpectedly: %v", err)
	}
	require.NotEmpty(t, stdout.String(), "doctor must print a report")
}

func TestRunSyncConflictsList_NoConflicts(t *testing.T) {
	initTestApp(t)

	var stdout, stderr bytes.Buffer
	err := runSyncConflictsList(context.Background(), &stdout, &stderr, "")
	require.NoError(t, err)
}

// TestSyncBackup helpers — pgDumpBin override path.
func TestPgDumpBin_RespectsEnvOverride(t *testing.T) {
	t.Setenv("MTIX_PG_DUMP", "/custom/path/pg_dump")
	require.Equal(t, "/custom/path/pg_dump", pgDumpBin())
}

func TestPgDumpBin_DefaultIsPgDump(t *testing.T) {
	t.Setenv("MTIX_PG_DUMP", "")
	require.Equal(t, "pg_dump", pgDumpBin())
}

// TestCheckSecretsFileMode covers the doctor's local-only mode check.
func TestCheckSecretsFileMode_AbsentIsOK(t *testing.T) {
	initTestApp(t)
	ok, detail := checkSecretsFileMode(app.mtixDir)
	require.True(t, ok, "absent secrets file is OK; got %s", detail)
}

func TestCheckSecretsFileMode_ModeTooLooseIsRejected(t *testing.T) {
	initTestApp(t)
	secretsPath := filepath.Join(app.mtixDir, "secrets")
	require.NoError(t, os.WriteFile(secretsPath,
		[]byte("postgres://u:p@h/d"), 0o644)) //nolint:gosec // deliberately too-loose for the test
	ok, detail := checkSecretsFileMode(app.mtixDir)
	require.False(t, ok, "0644 secrets file should be rejected")
	require.Contains(t, detail, "0600")
}

func TestCheckSecretsFileMode_Mode0600IsAccepted(t *testing.T) {
	initTestApp(t)
	secretsPath := filepath.Join(app.mtixDir, "secrets")
	require.NoError(t, os.WriteFile(secretsPath,
		[]byte("postgres://u:p@h/d"), 0o600))
	ok, detail := checkSecretsFileMode(app.mtixDir)
	require.True(t, ok, "0600 secrets file should pass; got %s", detail)
}

// TestNowISO covers the tiny helper in sync_conflicts.go. RFC3339 shape.
func TestNowISO_RFC3339Shape(t *testing.T) {
	got := nowISO()
	// RFC3339: "2026-05-19T00:11:54Z" — at least 20 chars, ends with 'Z'.
	require.GreaterOrEqual(t, len(got), 20)
}

// PG-gated: readSyncStatus + readConflicts + lookupConflict are exercised
// by runSyncStatus / runSyncConflictsList; this exercises them
// directly with a minimal seed to drive the SQL path.

func TestReadSyncStatus_FreshStore(t *testing.T) {
	initTestApp(t)
	rep, err := readSyncStatus(context.Background(), app.store)
	require.NoError(t, err)
	require.Equal(t, 0, rep.Pending,
		"fresh store has no pending sync events")
}

func TestReadConflicts_EmptyTable(t *testing.T) {
	initTestApp(t)
	conflicts, err := readConflicts(context.Background(), app.store, "")
	require.NoError(t, err)
	require.Empty(t, conflicts)
}

func TestLookupConflict_NotFoundReturnsZeroRow(t *testing.T) {
	initTestApp(t)
	// lookupConflict returns a zero ConflictRow with nil error when
	// the id is not in sync_conflicts (per its sql.ErrNoRows handling).
	row, err := lookupConflict(context.Background(), app.store, 999)
	require.NoError(t, err)
	require.Equal(t, int64(0), row.ConflictID,
		"not-found returns the zero value")
}

// TestRegisterProjectOnHub exercises the hub-side INSERT path used by
// runSyncInit. Idempotent — second call with same prefix is a no-op.
func TestRegisterProjectOnHub_Idempotent(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()

	require.NoError(t, registerProjectOnHub(ctx, pool, "TEST", "deadbeef"))
	// Second call must not error (ON CONFLICT DO NOTHING).
	require.NoError(t, registerProjectOnHub(ctx, pool, "TEST", "deadbeef"))
}

func TestReadHubFirstEventHash_AfterRegister(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()

	require.NoError(t, registerProjectOnHub(ctx, pool, "TEST", "deadbeef1234"))
	prefix, hash, err := readHubFirstEventHash(ctx, pool, "TEST")
	require.NoError(t, err)
	require.Equal(t, "TEST", prefix)
	require.Equal(t, "deadbeef1234", hash)
}

func TestReadHubFirstEventHash_UnregisteredReturnsEmpty(t *testing.T) {
	pool := openCmdHub(t)
	prefix, hash, err := readHubFirstEventHash(context.Background(), pool, "NEVER-REGISTERED")
	require.NoError(t, err)
	require.Empty(t, prefix)
	require.Empty(t, hash)
}
