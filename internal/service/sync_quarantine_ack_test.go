// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

const quarantineAckEventID = "0193fb00-0000-7000-8000-000000000001"
const quarantineAckInvalidRaw = `{"project_prefix":"TEST","author_id":"peer","author_machine_hash":"0123456789abcdef","vector_clock":{},"lamport_clock":1,"event_id":"0193fb00-0000-7000-8000-000000000001","node_id":"TEST-1é","op_type":"create_node","payload":{"title":"held"}}`

type quarantineAckCase struct {
	name, source, raw, storedReason, actor, reason string
	allowed                                        bool
}

func quarantineAckEligibilityCases() []quarantineAckCase {
	valid := `{"project_prefix":"TEST","author_id":"peer","author_machine_hash":"0123456789abcdef","vector_clock":{},"lamport_clock":1,"event_id":"0193fb00-0000-7000-8000-000000000001","node_id":"TEST-1","op_type":"create_node","payload":{"title":"valid"}}`
	malformedPayload := `{"project_prefix":"TEST","author_id":"peer","author_machine_hash":"0123456789abcdef","vector_clock":{},"lamport_clock":1,"event_id":"0193fb00-0000-7000-8000-000000000001","node_id":"TEST-1","op_type":"create_node","payload":{"parent_id":42}}`
	mismatch := `{"project_prefix":"TEST","author_id":"peer","author_machine_hash":"0123456789abcdef","vector_clock":{},"lamport_clock":1,"event_id":"0193fb00-0000-7000-8000-000000000002","node_id":"TEST-1é","op_type":"create_node","payload":{"title":"held"}}`
	return []quarantineAckCase{
		{"pull invalid id", "pull", quarantineAckInvalidRaw, "invalid node id", "op-a", "reviewed", true},
		{"sweep invalid id", "sweep", quarantineAckInvalidRaw, "invalid node id", "op-a", "reviewed", true},
		{"eligibility ignores stored wording", "pull", quarantineAckInvalidRaw, "earlier other check", "op-a", "reviewed", true},
		{"reason text cannot grant eligibility", "pull", valid, "invalid node id", "op-a", "reviewed", false},
		{"push hold", "push", quarantineAckInvalidRaw, "invalid node id", "op-a", "reviewed", false},
		{"malformed JSON", "pull", "not-json", "invalid node id", "op-a", "reviewed", false},
		{"payload decode error without id error", "pull", malformedPayload, "invalid node id", "op-a", "reviewed", false},
		{"raw event id differs", "pull", mismatch, "invalid node id", "op-a", "reviewed", false},
		{"blank reason", "pull", quarantineAckInvalidRaw, "invalid node id", "op-a", "", false},
		{"whitespace reason", "pull", quarantineAckInvalidRaw, "invalid node id", "op-a", " \t\n ", false},
		{"missing actor", "pull", quarantineAckInvalidRaw, "invalid node id", "", "reviewed", false},
		{"invalid actor", "pull", quarantineAckInvalidRaw, "invalid node id", "UpperCase", "reviewed", false},
		{"Unicode whitespace reason", "pull", quarantineAckInvalidRaw, "invalid node id", "op-a", "\u2003\u00a0", false},
	}
}

func seedQuarantineAck(t *testing.T, st *sqlite.Store, tc quarantineAckCase) {
	t.Helper()
	require.NoError(t, st.WithTx(t.Context(), func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(t.Context(), tx, sqlite.QuarantinedEvent{
			EventID: quarantineAckEventID, Source: tc.source, RawEvent: tc.raw,
			Reason: tc.storedReason, FirstSeen: "2026-03-14T10:00:00Z", LastAttempt: "2026-03-14T10:00:00Z",
		})
	}))
}

func quarantineAckAuditCount(t *testing.T, st *sqlite.Store) int {
	t.Helper()
	var n int
	require.NoError(t, st.QueryRow(t.Context(), "SELECT COUNT(*) FROM sync_quarantine_acknowledgements").Scan(&n))
	return n
}

func TestSyncServiceAcknowledgeQuarantine_StoredRawEligibility_Atomic(t *testing.T) {
	for _, tc := range append(append(quarantineAckEligibilityCases(), quarantineAckReferenceCases()...), quarantineAckMalformedCases()...) {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, _ := newTestSyncService(t)
			seedQuarantineAck(t, st, tc)
			before, err := st.QuarantinePage(t.Context(), nil, 10)
			require.NoError(t, err)
			_, err = svc.AcknowledgeQuarantine(t.Context(), quarantineAckEventID, tc.actor, tc.reason)
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, 1, quarantineAckAuditCount(t, st))
			} else {
				require.Error(t, err)
				require.Zero(t, quarantineAckAuditCount(t, st))
			}
			after, readErr := st.QuarantinePage(t.Context(), nil, 10)
			require.NoError(t, readErr)
			// Compare existing row fields, excluding future optional acknowledgement metadata.
			require.Equal(t, before[0].RawEvent, after[0].RawEvent)
			require.Equal(t, before[0].Reason, after[0].Reason)
			require.Equal(t, before[0].Attempts, after[0].Attempts)
			require.Equal(t, before[0].LastAttempt, after[0].LastAttempt)
		})
	}
}

func TestSyncServiceAcknowledgeQuarantine_Repeated_ImmutableInjectedReceipt(t *testing.T) {
	svc, st, _ := newTestSyncService(t)
	tc := quarantineAckEligibilityCases()[0]
	seedQuarantineAck(t, st, tc)
	got, err := svc.AcknowledgeQuarantine(t.Context(), quarantineAckEventID, "op-first", "reviewed original")
	require.NoError(t, err)
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	sum := sha256.Sum256([]byte(tc.raw))
	require.Equal(t, "op-first", fields["actor"])
	require.Equal(t, "reviewed original", fields["reason"])
	require.Equal(t, "2026-03-14T12:00:00Z", fields["acknowledged_at"])
	require.Equal(t, hex.EncodeToString(sum[:]), fields["raw_event_sha256"])
	again, err := svc.AcknowledgeQuarantine(t.Context(), quarantineAckEventID, "op-second", "different reason")
	require.NoError(t, err)
	require.Equal(t, got.QuarantineAcknowledgement, again.QuarantineAcknowledgement, "the first immutable receipt is returned without replacement")
	require.False(t, got.AlreadyAcknowledged)
	require.True(t, again.AlreadyAcknowledged)
	require.Equal(t, 1, quarantineAckAuditCount(t, st))
}

func TestSyncServiceAcknowledgeQuarantine_AuditWriteFailure_NoAcknowledgement(t *testing.T) {
	svc, st, _ := newTestSyncService(t)
	seedQuarantineAck(t, st, quarantineAckEligibilityCases()[0])
	_, err := st.WriteDB().ExecContext(t.Context(), `CREATE TRIGGER deny_quarantine_ack
        BEFORE INSERT ON sync_quarantine_acknowledgements
        BEGIN SELECT RAISE(ABORT, 'owned acknowledgement write failure'); END`)
	require.NoError(t, err)
	_, err = svc.AcknowledgeQuarantine(t.Context(), quarantineAckEventID, "op-a", "reviewed")
	require.ErrorContains(t, err, "owned acknowledgement write failure")
	require.Zero(t, quarantineAckAuditCount(t, st))
	n, err := st.CountQuarantined(t.Context())
	require.NoError(t, err)
	require.Equal(t, 1, n)
}

func TestSyncServiceAcknowledgeQuarantine_Cancelled_NoAcknowledgement(t *testing.T) {
	svc, st, _ := newTestSyncService(t)
	seedQuarantineAck(t, st, quarantineAckEligibilityCases()[0])
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := svc.AcknowledgeQuarantine(ctx, quarantineAckEventID, "op-a", "reviewed")
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, quarantineAckAuditCount(t, st))
}

func TestSyncServiceAcknowledgeQuarantine_ChangedRawSameID_RequiresFreshReceipt(t *testing.T) {
	svc, st, _ := newTestSyncService(t)
	tc := quarantineAckEligibilityCases()[0]
	seedQuarantineAck(t, st, tc)
	first, err := svc.AcknowledgeQuarantine(t.Context(), quarantineAckEventID, "op-a", "reviewed")
	require.NoError(t, err)
	require.NoError(t, st.WithTx(t.Context(), func(tx *sql.Tx) error {
		return sqlite.RemoveQuarantined(t.Context(), tx, quarantineAckEventID)
	}))
	require.Equal(t, 1, quarantineAckAuditCount(t, st), "history survives quarantine removal")
	tc.raw = " \n" + tc.raw + "\n "
	seedQuarantineAck(t, st, tc)
	listed, err := svc.ListQuarantined(t.Context())
	require.NoError(t, err)
	require.Len(t, listed, 1)
	raw, err := json.Marshal(listed[0])
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	require.NotContains(t, fields, "acknowledgement", "an old receipt cannot acknowledge different exact bytes")
	second, err := svc.AcknowledgeQuarantine(t.Context(), quarantineAckEventID, "op-a", "reviewed")
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	require.Equal(t, 2, quarantineAckAuditCount(t, st))
	raw, err = json.Marshal(second)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &fields))
	sum := sha256.Sum256([]byte(tc.raw))
	require.Equal(t, hex.EncodeToString(sum[:]), fields["raw_event_sha256"])
}

func TestSyncServiceAcknowledgeQuarantine_UnknownID_NoReceipt(t *testing.T) {
	svc, st, _ := newTestSyncService(t)
	_, err := svc.AcknowledgeQuarantine(t.Context(), "unknown", "op-a", "reviewed")
	require.Error(t, err)
	require.Zero(t, quarantineAckAuditCount(t, st))
}

func quarantineAckReferenceCases() []quarantineAckCase {
	prefix := `{"project_prefix":"TEST","author_id":"peer","author_machine_hash":"0123456789abcdef","vector_clock":{},"lamport_clock":1,"event_id":"` + quarantineAckEventID + `","node_id":"TEST-1","op_type":"`
	return []quarantineAckCase{
		{"invalid create parent", "pull", prefix + `create_node","payload":{"parent_id":"TEST-2é"}}`, "other check", "op-a", "reviewed", true},
		{"invalid link target", "sweep", prefix + `link_dep","payload":{"depends_on_node_id":"TEST-2é"}}`, "other check", "op-a", "reviewed", true},
		{"invalid unlink target", "pull", prefix + `unlink_dep","payload":{"depends_on_node_id":"TEST-2é"}}`, "other check", "op-a", "reviewed", true},
		{"foreign field cannot hide parent", "pull", prefix + `create_node","payload":{"parent_id":"TEST-2é","depends_on_node_id":"TEST-2"}}`, "other check", "op-a", "reviewed", true},
		{"foreign invalid parent is unrelated", "pull", prefix + `link_dep","payload":{"parent_id":"TEST-2é","depends_on_node_id":"TEST-2"}}`, "invalid node id", "op-a", "reviewed", false},
	}
}

func TestSyncServiceAcknowledgeQuarantine_DoctorEligibility_UsesRawAndSource(t *testing.T) {
	cases := append(append(quarantineAckEligibilityCases()[:8], quarantineAckReferenceCases()...), quarantineAckMalformedCases()...)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := sqlite.QuarantinedEvent{EventID: quarantineAckEventID, Source: tc.source, RawEvent: tc.raw, Reason: "arbitrary stored wording"}
			require.Equal(t, tc.allowed, service.QuarantineAcknowledgementEligible(q))
		})
	}
}

func quarantineAckMalformedCases() []quarantineAckCase {
	raw := quarantineAckInvalidRaw
	marker := strings.TrimSuffix(raw, "}") + `,"malformed":"hub row did not decode"}`
	large := strings.Replace(raw, `"title":"held"`, `"title":"`+strings.Repeat("x", validator.MaxPayloadBytes+1)+`"`, 1)
	overflow := strings.Replace(raw, `"lamport_clock":1`, `"lamport_clock":`+strconv.FormatInt(validator.MaxLamportClock, 10), 1)
	return []quarantineAckCase{
		{"missing node id", "pull", strings.Replace(raw, `"node_id":"TEST-1é",`, "", 1), "invalid node id", "op-a", "reviewed", false},
		{"invalid operation", "pull", strings.Replace(raw, "create_node", "not_an_operation", 1), "invalid node id", "op-a", "reviewed", false},
		{"missing vector clock", "pull", strings.Replace(raw, `"vector_clock":{},`, "", 1), "invalid node id", "op-a", "reviewed", false},
		{"transport malformed marker", "sweep", marker, "invalid node id", "op-a", "reviewed", false},
		{"oversize envelope", "pull", large, "invalid node id", "op-a", "reviewed", false},
		{"overflow envelope", "pull", overflow, "invalid node id", "op-a", "reviewed", false},
	}
}

func TestSyncServiceAcknowledgeQuarantine_EmptyExistingID_Refused(t *testing.T) {
	svc, st, _ := newTestSyncService(t)
	raw := strings.Replace(quarantineAckInvalidRaw, quarantineAckEventID, "", 1)
	require.NoError(t, st.WithTx(t.Context(), func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(t.Context(), tx, sqlite.QuarantinedEvent{Source: "pull", RawEvent: raw})
	}))
	_, err := svc.AcknowledgeQuarantine(t.Context(), "", "op-a", "reviewed")
	require.Error(t, err)
	require.Zero(t, quarantineAckAuditCount(t, st))
}

// TestSyncServiceAcknowledgeQuarantine_ReasonLimit bounds immutable input (MTIX-187.2).
func TestSyncServiceAcknowledgeQuarantine_ReasonLimit(t *testing.T) {
	cases := []struct {
		name, reason string
		allowed      bool
	}{
		{"ASCII boundary", strings.Repeat("x", 500), true},
		{"ASCII too long", strings.Repeat("x", 501), false},
		{"UTF8 boundary", strings.Repeat("é", 250), true},
		{"UTF8 too long", strings.Repeat("é", 251), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, _ := newTestSyncService(t)
			seedQuarantineAck(t, st, quarantineAckEligibilityCases()[0])
			got, err := svc.AcknowledgeQuarantine(t.Context(), quarantineAckEventID, "operator", tc.reason)
			if tc.allowed {
				require.NoError(t, err)
				require.Equal(t, tc.reason, got.Reason)
			} else {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.Zero(t, quarantineAckAuditCount(t, st))
			}
		})
	}
}
