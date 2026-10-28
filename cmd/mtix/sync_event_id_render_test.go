// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// hostileEventID carries a color escape, a newline, a right-to-left override
// and non-ASCII text: the shapes a hub or a peer can put in an event id
// (MTIX-187.4.2, the MTIX-187.2.6.2.1 ruling: ids are stored as received and
// rendered safely on human-facing surfaces).
const hostileEventID = "ev-\x1b[31mred\nline2\u202e\u00e9"

// requirePrintableASCII fails when out holds a byte that is neither a line
// end nor printable ASCII, and when it does not show the safe rendering of id.
func requirePrintableASCII(t *testing.T, out, id string) {
	t.Helper()
	require.Contains(t, out, format.EventID(id), "output must hold the safe rendering of the id")
	for i := 0; i < len(out); i++ {
		c := out[i]
		require.Truef(t, c == '\n' || (c >= 0x20 && c <= 0x7e), "byte 0x%02x at %d is not printable ASCII in %q", c, i, out)
	}
}

func TestPrintQuarantineTable_HostileEventID_RendersPrintableASCII(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, printQuarantineTable(&out, []service.QuarantinedEvent{{EventID: hostileEventID, NodeID: "N-1", OpType: "create_node"}}))
	requirePrintableASCII(t, out.String(), hostileEventID)
	require.Len(t, strings.Split(strings.TrimRight(out.String(), "\n"), "\n"), 2, "header plus one row")
}

func TestReportQuarantined_HostileEventID_RendersPrintableASCII(t *testing.T) {
	e := &model.SyncEvent{EventID: hostileEventID}
	var out bytes.Buffer
	reportQuarantined(&out, "main", []*model.SyncEvent{e}, heldEvents{hostileEventID: errors.New("bad payload")})
	requirePrintableASCII(t, out.String(), hostileEventID)
	require.Len(t, strings.Split(strings.TrimRight(out.String(), "\n"), "\n"), 1)
}

func TestReportHolds_HostileEventID_RendersPrintableASCII(t *testing.T) {
	var out bytes.Buffer
	reportHolds(&out, []sqlite.QuarantinedEvent{{EventID: hostileEventID, NodeID: "N-1", OpType: "create_node", Reason: "held"}})
	requirePrintableASCII(t, out.String(), hostileEventID)
	require.Len(t, strings.Split(strings.TrimRight(out.String(), "\n"), "\n"), 1)
}

func TestCloneRefusal_HostileEventID_RendersPrintableASCII(t *testing.T) {
	err := cloneRefusal(&model.SyncEvent{EventID: hostileEventID}, errors.New("invalid payload"), false)
	requirePrintableASCII(t, err.Error(), hostileEventID)
	require.ErrorIs(t, err, errCloneRefused)
}

func TestHoldMismatches_HostileHubEventID_RendersPrintableASCII(t *testing.T) {
	_, err := holdMismatches(t.Context(), &bytes.Buffer{}, nil, nil,
		[]transport.PresenceMismatch{{EventID: hostileEventID, Fields: []string{"payload"}}})
	require.Error(t, err)
	requirePrintableASCII(t, err.Error(), hostileEventID)
}

func TestPrintStatusRepairDiffs_HostileEventIDs_RenderPrintableASCII(t *testing.T) {
	var out bytes.Buffer
	printStatusRepairDiffs(&out, []service.StatusRepairDiff{{
		NodeID: "N-1", WinnerEventID: hostileEventID, MatchedEventID: hostileEventID + "-m", Reason: "replay",
	}})
	requirePrintableASCII(t, out.String(), hostileEventID)
	require.Contains(t, out.String(), format.EventID(hostileEventID+"-m"))
}

func TestSyncQuarantineAcknowledge_HostileEventID_TextSafeAndJSONUnchanged(t *testing.T) {
	initAckCLI(t)
	seedAckCLI(t, hostileEventID, "pull", hostileRaw(t), "invalid node id")
	var text bytes.Buffer
	require.NoError(t, runSyncQuarantineAcknowledge(t.Context(), &text, hostileEventID, "reviewed"))
	requirePrintableASCII(t, text.String(), hostileEventID)

	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	var out bytes.Buffer
	require.NoError(t, runSyncQuarantineAcknowledge(t.Context(), &out, hostileEventID, "reviewed"))
	var receipt map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &receipt))
	require.Equal(t, hostileEventID, receipt["event_id"], "stored id is unchanged in JSON")
	want, err := json.Marshal(hostileEventID)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"event_id":`+string(want), "JSON keeps the plain JSON encoding of the id")
	require.NotContains(t, out.String(), `\"`, "JSON must not carry the quoted display form")
}

func TestRunSyncQuarantineList_HostileEventID_TextSafeAndJSONUnchanged(t *testing.T) {
	initAckCLI(t)
	seedAckCLI(t, hostileEventID, "pull", hostileRaw(t), "invalid node id")
	var text bytes.Buffer
	require.NoError(t, runSyncQuarantineList(t.Context(), &text))
	for _, line := range strings.Split(text.String(), "\n") {
		if strings.Contains(line, format.EventID(hostileEventID)) {
			// The id column only: the node column is outside this ticket.
			requirePrintableASCII(t, strings.SplitN(line, "  ", 2)[0], hostileEventID)
		}
	}
	require.Contains(t, text.String(), format.EventID(hostileEventID))

	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })
	var out bytes.Buffer
	require.NoError(t, runSyncQuarantineList(t.Context(), &out))
	want, err := json.Marshal(hostileEventID)
	require.NoError(t, err)
	require.Contains(t, out.String(), `"event_id": `+string(want))
	var rows []map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
	found := false
	for _, r := range rows {
		found = found || r["event_id"] == hostileEventID
	}
	require.True(t, found, "the stored id is listed unchanged")
}

// TestEventIDPrints_NoRawEventIDInFormattedText covers the print sites a unit
// test cannot reach cheaply (batch apply, admit, renumber, hold encode and the
// sweep resume lines): no formatting call in cmd/mtix may take an event id
// field, raw or through oneLine, as an argument. They go through
// format.EventID. A site that bypasses the helper fails this test.
func TestEventIDPrints_NoRawEventIDInFormattedText(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var bad []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		parsed, perr := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, perr)
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isFormattingCall(call) {
				return true
			}
			for _, a := range call.Args {
				if rawEventIDArg(a) {
					bad = append(bad, fset.Position(a.Pos()).String())
				}
			}
			return true
		})
	}
	require.Empty(t, bad, "event ids must be rendered with format.EventID in formatted text")
}

func isFormattingCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Errorf", "Fprintf", "Sprintf", "Printf":
		return true
	}
	return false
}

// rawEventIDArg reports whether a is an event id field (EventID, WinnerEventID,
// MatchedEventID) used as is or wrapped in oneLine.
func rawEventIDArg(a ast.Expr) bool {
	if call, ok := a.(*ast.CallExpr); ok {
		if id, isIdent := call.Fun.(*ast.Ident); isIdent && id.Name == "oneLine" && len(call.Args) == 1 {
			return rawEventIDArg(call.Args[0])
		}
		return false
	}
	sel, ok := a.(*ast.SelectorExpr)
	return ok && strings.HasSuffix(sel.Sel.Name, "EventID")
}

// hostileRaw is ackCLIRaw carrying hostileEventID as its event_id, so the
// acknowledgement's raw-event check accepts the quarantined row.
func hostileRaw(t *testing.T) string {
	t.Helper()
	id, err := json.Marshal(hostileEventID)
	require.NoError(t, err)
	return strings.Replace(ackCLIRaw, `"event_id":"`+ackCLIEventID+`"`, `"event_id":`+string(id), 1)
}
