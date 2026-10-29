//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// The documented recovery uses the actual CLI against a migrated test hub.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/validator"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

type quarantineRecoveryCLI struct {
	binary, project string
	env             []string
	hub             *transport.Pool
}

type quarantineCLIOutput struct {
	stdout, stderr string
	exitCode       int
}

func buildQuarantineRecoveryCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mtix")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "build actual CLI: %s", output)
	return binary
}

func newQuarantineRecoveryCLI(t *testing.T, binary string) quarantineRecoveryCLI {
	t.Helper()
	hub := openCmdHub(t)
	initTestApp(t)
	operator := operatorTestHome(t)
	f := quarantineRecoveryCLI{binary: binary, project: filepath.Dir(app.mtixDir), hub: hub}
	f.env = append(hostguard.Environ(), "MTIX_AUTHOR_ID=replica-op", "XDG_CONFIG_HOME="+operator, "APPDATA="+operator)
	out := f.run(t, "sync", "init", "--insecure-tls")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	return f
}

func (f quarantineRecoveryCLI) run(t *testing.T, args ...string) quarantineCLIOutput {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), f.binary, args...)
	cmd.Dir, cmd.Env = f.project, f.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit, "actual CLI process must launch")
		code = exit.ExitCode()
	}
	return quarantineCLIOutput{stdout.String(), stderr.String(), code}
}

func (f quarantineRecoveryCLI) doctor(t *testing.T, wantCode int) DoctorReport {
	t.Helper()
	out := f.run(t, "sync", "doctor", "--insecure-tls", "--json")
	require.Equal(t, wantCode, out.exitCode, "%s", out.stderr)
	var report DoctorReport
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &report), "%s", out.stdout)
	require.Equal(t, wantCode == 0, report.OverallPass)
	return report
}

func seedInvalidQuarantineThroughHub(t *testing.T, f quarantineRecoveryCLI) *model.SyncEvent {
	t.Helper()
	create := offlineEvents(t)[0]
	bad := invalidIDEvent(create, model.OpCreateNode, "TEST-1é", `{"title":"retained","node_type":"issue"}`)
	require.ErrorIs(t, validator.ValidateEventNodeIDs(bad), validator.ErrInvalidNodeID)
	result, err := f.hub.PushEventsResult(t.Context(), []*model.SyncEvent{bad})
	require.NoError(t, err)
	require.Equal(t, []string{bad.EventID}, result.Inserted)
	out := f.run(t, "sync", "pull", "--insecure-tls")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	requireNotApplied(t, bad)
	require.Equal(t, "pull", quarantined(t)[bad.EventID].Source)
	return bad
}

func quarantineRecoveryAckFromList(t *testing.T, f quarantineRecoveryCLI, eventID string) map[string]any {
	t.Helper()
	out := f.run(t, "sync", "quarantine", "list", "--json")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	var rows []map[string]any
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &rows))
	for _, row := range rows {
		if row["event_id"] == eventID {
			ack, ok := row["acknowledgement"].(map[string]any)
			require.True(t, ok, "retained event must display its actual receipt")
			return ack
		}
	}
	t.Fatalf("acknowledged event %s must remain listed", eventID)
	return nil
}

func requireRecoveredQuarantineRetained(t *testing.T, f quarantineRecoveryCLI, e *model.SyncEvent, raw string) {
	t.Helper()
	require.Equal(t, raw, quarantined(t)[e.EventID].RawEvent)
	requireNotApplied(t, e)
	_, err := app.store.GetNode(t.Context(), e.NodeID)
	require.ErrorIs(t, err, model.ErrNotFound)
	report := f.doctor(t, 0)
	requireQuarantineRecoverySurfaces(t, f)
	for _, check := range report.Checks {
		require.True(t, check.Pass, "%s: %s", check.Name, check.Detail)
		if check.Name == quarantineCheckName {
			require.False(t, check.Warn, "acknowledged-only quarantine check passes")
			require.Contains(t, check.Detail, "1 acknowledged")
		}
	}
}

func TestSyncQuarantineAcknowledge_InvalidID_DocumentedCLIRecoversDoctor(t *testing.T) {
	_ = requireCmdPG(t)
	binary := buildQuarantineRecoveryCLI(t)
	f := newQuarantineRecoveryCLI(t, binary)
	f.doctor(t, 0)
	bad := seedInvalidQuarantineThroughHub(t, f)
	original := quarantined(t)[bad.EventID]
	before := readPeerState(t, &sweepFixture{})
	hubBefore := quarantineRecoveryHubRows(t, f)
	report := f.doctor(t, 2)
	check := quarantineRecoveryDoctorCheck(t, report, quarantineCheckName)
	require.False(t, check.Pass)
	out := f.run(t, "sync", "quarantine", "acknowledge", bad.EventID, "--reason", "Reported retained invalid id to hub maintainer")
	require.Zero(t, out.exitCode, "documented recovery: %s", out.stderr)
	require.Contains(t, check.Fix, "mtix sync quarantine acknowledge")
	require.Equal(t, before, readPeerState(t, &sweepFixture{}))
	requireOriginalQuarantineRow(t, original, quarantined(t)[bad.EventID])
	require.Equal(t, hubBefore, quarantineRecoveryHubRows(t, f))
	requireRecoveredQuarantineRetained(t, f, bad, original.RawEvent)
	ack := quarantineRecoveryAckFromList(t, f, bad.EventID)
	requireQuarantineRecoveryReceipt(t, ack, original.RawEvent)
	out = f.run(t, "sync", "pull", "--insecure-tls")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	requireRecoveredQuarantineRetained(t, f, bad, original.RawEvent)
	require.Equal(t, ack, quarantineRecoveryAckFromList(t, f, bad.EventID))
	require.Equal(t, hubBefore, quarantineRecoveryHubRows(t, f))
}

func quarantineRecoveryHubRows(t *testing.T, f quarantineRecoveryCLI) []string {
	t.Helper()
	rows, err := f.hub.Inner().Query(t.Context(), "SELECT row_to_json(e)::text FROM sync_events e ORDER BY event_id")
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		require.NoError(t, rows.Scan(&raw))
		out = append(out, raw)
	}
	require.NoError(t, rows.Err())
	return out
}

func quarantineRecoveryDoctorCheck(t *testing.T, report DoctorReport, name string) DoctorCheck {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("missing doctor check %s", name)
	return DoctorCheck{}
}

func requireQuarantineRecoveryReceipt(t *testing.T, ack map[string]any, raw string) {
	t.Helper()
	sum := sha256.Sum256([]byte(raw))
	require.Equal(t, hex.EncodeToString(sum[:]), ack["raw_event_sha256"])
	require.Equal(t, "replica-op", ack["actor"])
	require.Equal(t, "Reported retained invalid id to hub maintainer", ack["reason"])
	at, ok := ack["acknowledged_at"].(string)
	require.True(t, ok, "receipt includes its actual persisted time")
	_, err := time.Parse(time.RFC3339Nano, at)
	require.NoError(t, err)
}

func requireOriginalQuarantineRow(t *testing.T, before, after sqlite.QuarantinedEvent) {
	t.Helper()
	require.Equal(t, before.EventID, after.EventID)
	require.Equal(t, before.Source, after.Source)
	require.Equal(t, before.RawEvent, after.RawEvent)
	require.Equal(t, before.Reason, after.Reason)
	require.Equal(t, before.FirstSeen, after.FirstSeen)
	require.Equal(t, before.LastAttempt, after.LastAttempt)
	require.Equal(t, before.Attempts, after.Attempts)
	require.Equal(t, before.CLIVersion, after.CLIVersion)
	require.Equal(t, before.Lamport, after.Lamport)
	require.Equal(t, before.NodeID, after.NodeID)
	require.Equal(t, before.OpType, after.OpType)
}

func requireQuarantineRecoverySurfaces(t *testing.T, f quarantineRecoveryCLI) {
	t.Helper()
	out := f.run(t, "sync", "status", "--json")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	var st SyncStatus
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &st))
	require.Equal(t, 1, st.QuarantinedEvents)
	require.Equal(t, 1, st.AcknowledgedQuarantinedEvents)
	require.Zero(t, st.UnacknowledgedQuarantinedEvents)
	require.Equal(t, 1, st.QuarantinedInvalidIDs)
	out = f.run(t, "sync", "doctor", "--insecure-tls")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	require.Contains(t, out.stdout, "[PASS] quarantined events")
	require.Contains(t, out.stdout, "1 acknowledged")
}
