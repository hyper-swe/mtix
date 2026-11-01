// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// legacyCreationID is a creation id with non-ASCII but no control
// characters, so a reason that embeds it is stored byte for byte.
const legacyCreationID = "ev-\xc3\xa9-create"

// printableASCIILines fails when any byte of out is not a line end or
// printable ASCII.
func printableASCIILines(t *testing.T, out string) {
	t.Helper()
	for i := 0; i < len(out); i++ {
		c := out[i]
		require.Truef(t, c == '\n' || (c >= 0x20 && c <= 0x7e), "byte 0x%02x at %d is not printable ASCII in %q", c, i, out)
	}
}

func TestQuarantinedCreatePrefix_SentinelError_MatchesTextAndSpace(t *testing.T) {
	require.Equal(t, sqlite.ErrBehindQuarantinedCreate.Error()+" ", quarantinedCreatePrefix)
}

func TestRefuseBehindQuarantinedCreate_HostileCreationID_StoresRawReason(t *testing.T) {
	initAckCLI(t)
	seedAckCLI(t, legacyCreationID, "pull", `{"node_id":"TEST-1","op_type":"create_node"}`, "invalid payload")
	dependent := &model.SyncEvent{EventID: "ev-dep", NodeID: "TEST-1", OpType: model.OpClaim}
	var refused error
	require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		var err error
		refused, err = refuseBehindQuarantinedCreate(t.Context(), tx, dependent)
		return err
	}))
	require.Error(t, refused)
	require.ErrorIs(t, refused, sqlite.ErrBehindQuarantinedCreate)
	require.Equal(t, quarantinedCreatePrefix+legacyCreationID, quarantineReason(refused),
		"the stored reason keeps the id as received: it is rendered when printed, not when stored")
}

func TestQuarantineList_LegacyQuarantinedCreateReason_TextSafeAndJSONByteEqual(t *testing.T) {
	initAckCLI(t)
	stored := quarantinedCreatePrefix + legacyCreationID
	seedAckCLI(t, "ev-dep", "pull", `{"node_id":"TEST-1","op_type":"claim"}`, stored)

	var text bytes.Buffer
	require.NoError(t, runSyncQuarantineList(t.Context(), &text))
	// initAckCLI seeds another row whose node id is outside this ticket; only
	// the legacy row's line is checked.
	var line string
	for _, l := range strings.Split(text.String(), "\n") {
		if strings.Contains(l, quarantinedCreatePrefix) {
			line = l
		}
	}
	require.NotEmpty(t, line, "the legacy row is listed")
	printableASCIILines(t, line)
	require.Contains(t, line, quarantinedCreatePrefix+format.EventID(legacyCreationID))

	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	var out bytes.Buffer
	require.NoError(t, runSyncQuarantineList(t.Context(), &out))
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	var reasons []any
	for _, r := range rows {
		if r["event_id"] == "ev-dep" {
			reasons = append(reasons, r["reason"])
		}
	}
	require.Equal(t, []any{stored}, reasons, "the --json reason is the stored reason, byte for byte")
}

func TestDisplayHoldReason_Table_FoldsAndRendersEveryBranch(t *testing.T) {
	tests := []struct {
		name   string
		reason string
		want   string
	}{
		{"non-dependent reason is folded", "bad\x1b[1m payload\n  next\ttail", "bad[1m payload next tail"},
		{"dependent hold without tail", holdDependsPrefix + "ev-1", holdDependsPrefix + `"ev-1"`},
		{"dependent hold tail is folded", holdDependsPrefix + "ev-1 (N-1)\n  wait\x1b[0m", holdDependsPrefix + `"ev-1" (N-1) wait[0m`},
		{"dependent hold id is rendered", holdDependsPrefix + "ev-\xc3\xa9 (N-1)", holdDependsPrefix + `"ev-\u00e9" (N-1)`},
		{"dependency id with a space keeps the rest printable", holdDependsPrefix + "ev \xc3\xa9\xe2\x80\xae (N-1)",
			holdDependsPrefix + `"ev" \u00e9 (N-1)`},
		{"quarantined create id is rendered", quarantinedCreatePrefix + "ev-\xc3\xa9", quarantinedCreatePrefix + `"ev-\u00e9"`},
		{"quarantined create id with a space is kept whole", quarantinedCreatePrefix + "ev \xc3\xa9", quarantinedCreatePrefix + `"ev \u00e9"`},
		{"empty reason", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := displayHoldReason(tt.reason)
			require.Equal(t, tt.want, got)
			printableASCIILines(t, got)
			require.NotContains(t, got, "\n")
		})
	}
}

func TestQuarantineDisplayReason_AcknowledgedRow_RendersTheReasonSafely(t *testing.T) {
	q := service.QuarantinedEvent{
		EventID: "ev-dep", Reason: quarantinedCreatePrefix + legacyCreationID,
		Acknowledgement: &service.QuarantineAcknowledgement{
			Actor: "op", AcknowledgedAt: "2026-03-14T10:00:00Z", RawEventSHA256: "abc", Reason: "kept",
		},
	}
	got := quarantineDisplayReason(q)
	printableASCIILines(t, got)
	require.True(t, strings.HasPrefix(got, quarantinedCreatePrefix+format.EventID(legacyCreationID)+"; acknowledged by op"), got)
}

func TestApplyPulledEvent_HostileEventIDOnLocalFailure_RendersPrintableASCII(t *testing.T) {
	initTestApp(t)
	e := &model.SyncEvent{EventID: hostileEventID}
	err := app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		_, aerr := applyPulledEvent(cancelled, tx, e)
		return aerr
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "apply ")
	requirePrintableASCII(t, err.Error(), hostileEventID)
}

func TestRefuseBehindQuarantinedCreate_HostileCreationID_PrintsPrintableASCIIOnPull(t *testing.T) {
	initAckCLI(t)
	seedAckCLI(t, hostileEventID, "pull", `{"node_id":"TEST-1","op_type":"create_node"}`, "invalid payload")
	dependent := &model.SyncEvent{EventID: "ev-dep", NodeID: "TEST-1", OpType: model.OpClaim}
	var refused error
	require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		var err error
		refused, err = refuseBehindQuarantinedCreate(t.Context(), tx, dependent)
		return err
	}))
	require.Error(t, refused)
	var out bytes.Buffer
	reportQuarantined(&out, "main", []*model.SyncEvent{dependent}, heldEvents{"ev-dep": refused})
	printableASCIILines(t, out.String())
	require.Contains(t, out.String(), quarantinedCreatePrefix+format.EventID(oneLine(hostileEventID)))
}

// TestCloneRefusal_HostileDependencyReasons_RendersPrintableASCII covers the
// clone refusal's reason rendering separately from the refused event id
// (MTIX-187.4.13, mutant M18).
func TestCloneRefusal_HostileDependencyReasons_RendersPrintableASCII(t *testing.T) {
	for _, prefix := range []string{holdDependsPrefix, quarantinedCreatePrefix} {
		t.Run(prefix, func(t *testing.T) {
			id := strings.ReplaceAll(hostileEventID, "\n", "")
			raw := prefix + id
			reason := errors.New(raw)
			event := &model.SyncEvent{EventID: "ev-dependent"}
			for _, written := range []bool{false, true} {
				err := cloneRefusal(event, reason, written)
				require.ErrorIs(t, err, errCloneRefused)
				requirePrintableASCII(t, err.Error(), oneLine(id))
				require.Contains(t, err.Error(), prefix+format.EventID(oneLine(id)))
				require.NotContains(t, err.Error(), "\n", "a refusal stays on one line")
				require.Equal(t, raw, reason.Error(), "rendering keeps the original reason")
				require.Equal(t, "ev-dependent", event.EventID)
			}
		})
	}
}
