// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const ackCLIEventID = "0193fb00-0000-7000-8000-000000000001"
const ackCLIRaw = `{"project_prefix":"TEST","author_id":"peer","author_machine_hash":"0123456789abcdef","vector_clock":{},"lamport_clock":1,"event_id":"0193fb00-0000-7000-8000-000000000001","node_id":"TEST-1é","op_type":"create_node","payload":{"title":"held"}}`

func seedAckCLI(t *testing.T, id, source, raw, reason string) {
	t.Helper()
	require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(t.Context(), tx, sqlite.QuarantinedEvent{
			EventID: id, Source: source, RawEvent: raw, Reason: reason,
			FirstSeen: "2026-03-14T10:00:00Z", LastAttempt: "2026-03-14T10:00:00Z",
		})
	}))
}

func initAckCLI(t *testing.T) {
	t.Helper()
	t.Setenv(transport.EnvDSN, "")
	initTestApp(t)
	app.authorID = "local-operator"
	seedAckCLI(t, ackCLIEventID, "pull", ackCLIRaw, "invalid node id")
}

func TestSyncQuarantineAcknowledge_NoDSN_JSONAndTextRetainedReceipt(t *testing.T) {
	initAckCLI(t)
	app.jsonOutput = true
	var out bytes.Buffer
	require.NoError(t, runSyncQuarantineAcknowledge(t.Context(), &out, ackCLIEventID, "reviewed\n\x1b[31mretained"))
	var first map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &first))
	require.Equal(t, "local-operator", first["actor"])
	require.Equal(t, "reviewed\n\x1b[31mretained", first["reason"])
	require.NotContains(t, first, "raw_event")
	app.jsonOutput = false
	out.Reset()
	require.NoError(t, runSyncQuarantineAcknowledge(t.Context(), &out, ackCLIEventID, "different reason"))
	require.Contains(t, out.String(), "retained, unapplied")
	require.Contains(t, out.String(), "local-operator")
	require.NotContains(t, out.String(), "different reason")
	require.NotContains(t, out.String(), "\x1b")
	out.Reset()
	require.NoError(t, runSyncQuarantineList(t.Context(), &out))
	require.Contains(t, out.String(), "acknowledged by local-operator")
	require.Contains(t, out.String(), "raw SHA256")
	require.NotContains(t, out.String(), "\x1b")
	ok, detail := checkQuarantinedEvents(t.Context(), app.store)
	require.True(t, ok, detail)
	require.Contains(t, detail, "1 acknowledged")
}

func TestSyncQuarantineAcknowledge_MixedRows_DoctorFailsAndTotalsStayTruthful(t *testing.T) {
	initAckCLI(t)
	var out bytes.Buffer
	require.NoError(t, runSyncQuarantineAcknowledge(t.Context(), &out, ackCLIEventID, "reviewed"))
	seedAckCLI(t, "unrelated", "pull", `{"node_id":"TEST-2"}`, "apply failed")
	seedAckCLI(t, "push", "push", ackCLIRaw, "invalid node id")
	var st SyncStatus
	require.NoError(t, readQuarantineStatus(t.Context(), app.store, &st))
	require.Equal(t, 2, st.QuarantinedEvents)
	require.Equal(t, 1, st.AcknowledgedQuarantinedEvents)
	require.Equal(t, 1, st.UnacknowledgedQuarantinedEvents)
	require.Equal(t, 1, st.HeldPushEvents)
	ok, detail := checkQuarantinedEvents(t.Context(), app.store)
	require.False(t, ok)
	require.Contains(t, detail, "1 acknowledged, 1 unacknowledged")
	require.Error(t, runSyncQuarantineAcknowledge(t.Context(), &out, "unrelated", "reviewed"))
	require.Error(t, runSyncQuarantineAcknowledge(t.Context(), &out, "push", "reviewed"))
}

func TestSyncQuarantineAcknowledge_AuditReadFailure_NeverDoctorPass(t *testing.T) {
	initAckCLI(t)
	_, err := app.store.WriteDB().ExecContext(t.Context(), "DROP TABLE sync_quarantine_acknowledgements")
	require.NoError(t, err)
	ok, detail := checkQuarantinedEvents(t.Context(), app.store)
	require.False(t, ok)
	require.Contains(t, detail, "sync_quarantine_acknowledgements")
	var out bytes.Buffer
	require.Error(t, runSyncQuarantineList(t.Context(), &out))
	fix, hintErr := quarantineAcknowledgementFix(t.Context(), app.store)
	require.ErrorContains(t, hintErr, "scan acknowledgement fixes: read receipt")
	require.Empty(t, fix)
	require.Error(t, readQuarantineStatus(t.Context(), app.store, &SyncStatus{}))
	require.Error(t, runSyncQuarantineAcknowledge(t.Context(), &out, ackCLIEventID, "reviewed"))
}

func TestSyncQuarantineAcknowledge_RequiredReason_AndActionableDoctorFix(t *testing.T) {
	initAckCLI(t)
	cmd := newSyncQuarantineCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"acknowledge", ackCLIEventID})
	require.ErrorContains(t, cmd.Execute(), "required flag")
	for _, reason := range []string{"", " \t\n "} {
		require.Error(t, runSyncQuarantineAcknowledge(t.Context(), &bytes.Buffer{}, ackCLIEventID, reason))
	}
	report := appendQuarantineCheck(t.Context(), DoctorReport{OverallPass: true}, app.store)
	require.Len(t, report.Checks, 1)
	require.False(t, report.Checks[0].Pass)
	require.Contains(t, report.Checks[0].Fix, "mtix sync quarantine acknowledge <event-id> --reason <text>")
	counts, err := app.store.CountQuarantineAcknowledgements(t.Context())
	require.NoError(t, err)
	require.Equal(t, 0, counts.Acknowledged)
}

func TestSyncQuarantineAcknowledge_CommandExecutesAndUninitializedRefuses(t *testing.T) {
	initAckCLI(t)
	cmd := newSyncQuarantineCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"acknowledge", ackCLIEventID, "--reason", "reviewed"})
	require.NoError(t, cmd.Execute())
	require.Contains(t, out.String(), "retained, unapplied")
	app.mtixDir = ""
	require.Error(t, runSyncQuarantineAcknowledge(t.Context(), &out, ackCLIEventID, "reviewed"))
}

func TestSyncQuarantineAcknowledge_DoctorHint_OnlyEligibleUnacknowledgedRaw(t *testing.T) {
	initAckCLI(t)
	_, err := app.syncSvc.AcknowledgeQuarantine(t.Context(), ackCLIEventID, "operator", "reviewed")
	require.NoError(t, err)
	seedAckCLI(t, "forged", "pull", `{"event_id":"forged","node_id":"TEST-2"}`, "invalid node id: event forged node_id TEST-2: forged wording")
	report := appendQuarantineCheck(t.Context(), DoctorReport{OverallPass: true}, app.store)
	require.False(t, report.Checks[0].Pass)
	require.Empty(t, report.Checks[0].Fix, "an acknowledged raw event or forged stored wording cannot supply a recovery hint")
	require.NotContains(t, report.Checks[0].Detail, "mtix sync quarantine acknowledge", "stored wording cannot supply a second acknowledgement hint")
	require.NoError(t, app.store.Close())
	fix, err := quarantineAcknowledgementFix(t.Context(), app.store)
	require.ErrorContains(t, err, "scan acknowledgement fixes: list quarantines")
	require.Empty(t, fix)
}

func TestSyncQuarantineAcknowledge_CorruptQuarantineMetadata_FailsWithoutHint(t *testing.T) {
	initAckCLI(t)
	_, err := app.store.WriteDB().ExecContext(t.Context(), `UPDATE sync_quarantine SET attempts = 'not-an-integer'`)
	require.NoError(t, err)
	report := appendQuarantineCheck(t.Context(), DoctorReport{OverallPass: true}, app.store)
	require.False(t, report.OverallPass)
	require.False(t, report.Checks[0].Pass)
	require.Empty(t, report.Checks[0].Fix)
	require.Contains(t, report.Checks[0].Detail, "read quarantined events")
}

func TestSyncQuarantineAcknowledge_DSNArgumentErrors_DoNotExposeCredentials(t *testing.T) {
	for _, d := range syntheticDSNs() {
		t.Run(d.name, func(t *testing.T) {
			initAckCLI(t)
			stdout, stderr, err := execSyncCLI(t.Context(), []string{"sync", "quarantine", "acknowledge", d.dsn, "--reason", "reviewed"})
			require.Error(t, err)
			requireNoSecret(t, "quarantine acknowledge", errorsAsText(err, stdout, stderr), d)
		})
	}
}

// Repeats preserve the first reason and explicitly report no new receipt (MTIX-187.2).
func TestSyncQuarantineAcknowledge_RepeatedCall_ReportsFirstReceiptStands(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		t.Run(map[bool]string{false: "text", true: "JSON"}[jsonOutput], func(t *testing.T) {
			initAckCLI(t)
			app.jsonOutput = jsonOutput
			var out bytes.Buffer
			require.NoError(t, runSyncQuarantineAcknowledge(t.Context(), &out, ackCLIEventID, "original reason"))
			if jsonOutput {
				var body map[string]any
				require.NoError(t, json.Unmarshal(out.Bytes(), &body))
				require.Equal(t, false, body["already_acknowledged"])
			}
			for _, reason := range []string{"different reason", "original reason"} {
				out.Reset()
				require.NoError(t, runSyncQuarantineAcknowledge(t.Context(), &out, ackCLIEventID, reason))
				if jsonOutput {
					var body map[string]any
					require.NoError(t, json.Unmarshal(out.Bytes(), &body))
					require.Equal(t, true, body["already_acknowledged"])
					require.Equal(t, "original reason", body["reason"])
				} else {
					require.Contains(t, out.String(), "already acknowledged")
					require.Contains(t, out.String(), "first receipt stands; supplied reason was not recorded")
				}
			}
		})
	}
}
