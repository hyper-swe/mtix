// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
			// The event id column (second, after the handle): the node
			// column is outside this ticket. The quoted form holds no spaces.
			requirePrintableASCII(t, strings.Fields(line)[1], hostileEventID)
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

// TestEventIDPrints_NoRawEventIDInFormattedText backs the behavioural tests
// with a scan of cmd/mtix: no formatting call (fmt.Errorf, Fprint*, Sprint*,
// Print*) may carry an event id, in any wrapper (oneLine, string, strings.*,
// concatenation), unless it sits directly inside format.EventID. An event id
// is a selector or identifier named like one (EventID, WinnerEventID, eventID,
// or the id fields and locals listed in eventIDNames). The scan must see
// files, formatting calls and format.EventID calls, so it cannot pass by
// finding nothing.
func TestEventIDPrints_NoRawEventIDInFormattedText(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	fset := token.NewFileSet()
	var bad []string
	scanned, calls, safe := 0, 0, 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		parsed, perr := parser.ParseFile(fset, f, nil, 0)
		require.NoError(t, perr)
		sites, c, s := scanRawEventIDs(parsed, f)
		calls, safe = calls+c, safe+s
		for _, pos := range sites {
			bad = append(bad, fset.Position(pos).String())
		}
	}
	require.Greater(t, scanned, 50, "the scan must see the package")
	require.Greater(t, calls, 100, "the scan must see formatting calls")
	require.Greater(t, safe, 20, "the scan must see format.EventID calls")
	require.Empty(t, bad, "event ids must be rendered with format.EventID in formatted text")
}

// scanRawEventIDs returns the positions in file where an event id reaches
// text outside format.EventID: as an argument of a formatting call or of
// errors.New, or inside a string concatenation. It also counts the formatting
// calls and format.EventID calls it saw. Functions in rawIDFuncs are skipped.
func scanRawEventIDs(file *ast.File, name string) (sites []token.Pos, calls, safe int) {
	seen := map[token.Pos]bool{}
	add := func(a ast.Expr) {
		for _, pos := range rawEventIDUses(a, name) {
			if !seen[pos] {
				seen[pos] = true
				sites = append(sites, pos)
			}
		}
	}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && rawIDFuncs[fn.Name.Name] {
			continue
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.CallExpr:
				if isFormatEventIDCall(v) {
					safe++
				}
				if isFormattingCall(v) || isErrorsNewCall(v) {
					calls++
					for _, a := range v.Args {
						add(a)
					}
				}
			case *ast.BinaryExpr:
				if v.Op == token.ADD && hasStringLiteral(v) {
					add(v)
				}
			}
			return true
		})
	}
	return sites, calls, safe
}

// hasStringLiteral reports whether a concatenation has a string literal
// operand, at any depth of the chain.
func hasStringLiteral(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING
	case *ast.BinaryExpr:
		return v.Op == token.ADD && (hasStringLiteral(v.X) || hasStringLiteral(v.Y))
	case *ast.ParenExpr:
		return hasStringLiteral(v.X)
	}
	return false
}

func isErrorsNewCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "New" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "errors"
}

// rawIDFuncs are functions that build a stored reason from an id on purpose:
// refuseBehindQuarantinedCreate stores the creation id as received, and the
// id is rendered when the reason is printed (displayHoldReason).
// quarantineAcknowledgementCommand builds a copyable shell command that must
// carry the id itself; recoveryHintNeedsInspection sends any id with a
// non-printable rune to the quoted form instead (MTIX-187.2.6.3). A printable
// non-ASCII id still appears raw there until MTIX-187.7.6.
var rawIDFuncs = map[string]bool{
	"refuseBehindQuarantinedCreate":    true,
	"quarantineAcknowledgementCommand": true,
}

// TestScanRawEventIDs_FlagsEveryWrapperAndPassesSafeRendering is the positive
// control of the scan: each bypass shape is flagged, each safe form is not.
func TestScanRawEventIDs_FlagsEveryWrapperAndPassesSafeRendering(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"errors.New with a bare id", `errors.New(e.EventID)`, 1},
		{"errors.New with concatenation", `errors.New("apply " + e.EventID)`, 1},
		{"errors.Join over errors.New", `errors.Join(errors.New("apply "+e.EventID), err)`, 1},
		{"bare concatenation", `x := "apply " + e.EventID; _ = x`, 1},
		{"fmt.Errorf local id", `fmt.Errorf("apply %s", id)`, 1},
		{"Sprint of a field", `fmt.Sprint(h.EventID)`, 1},
		{"TrimSpace wrapper", `fmt.Sprintf("%s", strings.TrimSpace(h.EventID))`, 1},
		{"string conversion", `fmt.Sprintf("%s", string(e.EventID))`, 1},
		{"conflict winner field", `fmt.Fprintf(w, "winner=%s", r.EventIDWinner)`, 1},
		{"conflict loser through a truncating helper", `fmt.Fprintf(w, "loser=%s", shortHashForCLI(r.EventIDLoser))`, 1},
		{"conflict winner and loser in one call", `fmt.Fprintf(w, "%s %s", shortHashForCLI(r.EventIDWinner), r.EventIDLoser)`, 2},
		{"suffix-form field", `fmt.Sprint(d.WinnerEventID)`, 1},
		{"infix-form field", `fmt.Sprint(d.PriorEventIDWinner)`, 1},
		{"conflict winner rendered safely", `fmt.Fprintf(w, "winner=%s", format.EventID(r.EventIDWinner))`, 0},
		{"safe rendering in Errorf", `fmt.Errorf("apply %s", format.EventID(e.EventID))`, 0},
		{"safe rendering in concatenation", `errors.New("apply " + format.EventID(e.EventID))`, 0},
		{"no id", `errors.New("apply " + name)`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := "package main\nfunc f() {\n" + tt.body + "\n}\n"
			parsed, err := parser.ParseFile(token.NewFileSet(), "sync_probe.go", src, 0)
			require.NoError(t, err)
			sites, _, _ := scanRawEventIDs(parsed, "sync_probe.go")
			require.Len(t, sites, tt.want)
		})
	}
}

// eventIDNames are the identifiers and field names that hold a sync event id
// where the name alone does not say so.
var eventIDNames = map[string]bool{"eventID": true, "creation": true, "acceptedID": true}

func isFormattingCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Errorf", "Fprintf", "Sprintf", "Printf", "Fprint", "Fprintln", "Sprint", "Sprintln", "Print", "Println":
		return true
	}
	return false
}

// rawIDAllowed are files whose formatted ids are not display text of event
// ids: sync.go prints node ids; sync_push_hold_rule.go builds the stored hold
// reasons that heldFor parses back, which stay raw by design and are rendered
// at print time by displayHoldReason.
var rawIDAllowed = map[string]bool{"sync.go": true, "sync_push_hold_rule.go": true}

func isQuoteToASCII(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "QuoteToASCII" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "strconv"
}

func isFormatEventIDCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "EventID" {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "format"
}

// rawEventIDUses returns the positions in a where an event id appears
// outside a format.EventID call, however it is wrapped or joined.
func rawEventIDUses(a ast.Expr, file string) []token.Pos {
	if rawIDAllowed[file] {
		return nil
	}
	syncFile := strings.HasPrefix(file, "sync_")
	var out []token.Pos
	ast.Inspect(a, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if isFormatEventIDCall(v) || isQuoteToASCII(v) {
				return false
			}
		case *ast.SelectorExpr:
			if strings.Contains(strings.ToLower(v.Sel.Name), "eventid") || (syncFile && v.Sel.Name == "id") {
				out = append(out, v.Pos())
			}
		case *ast.Ident:
			if eventIDNames[v.Name] || (syncFile && v.Name == "id") {
				out = append(out, v.Pos())
			}
		}
		return true
	})
	return out
}

// hostileRaw is ackCLIRaw carrying hostileEventID as its event_id, so the
// acknowledgement's raw-event check accepts the quarantined row.
func hostileRaw(t *testing.T) string {
	t.Helper()
	id, err := json.Marshal(hostileEventID)
	require.NoError(t, err)
	return strings.Replace(ackCLIRaw, `"event_id":"`+ackCLIEventID+`"`, `"event_id":`+string(id), 1)
}

// fakeRenumberPusher answers every push with one renumber request for a
// hostile event id and nothing accepted.
type fakeRenumberPusher struct{}

func (fakeRenumberPusher) PushEventsResult(context.Context, []*model.SyncEvent) (transport.PushResult, error) {
	return transport.PushResult{Renumbers: []transport.RenumberRequired{{EventID: hostileEventID}}}, nil
}

func TestPushBatch_HostileRenumberEventID_RendersPrintableASCII(t *testing.T) {
	initTestApp(t)
	_, _, err := pushBatch(t.Context(), &bytes.Buffer{}, fakeRenumberPusher{}, app.store, nil, 1)
	require.Error(t, err)
	// Only this layer's text: the store's own "renumber uid" error is the
	// MTIX-187.4.3 surface.
	mine, _, _ := strings.Cut(err.Error(), "(batch 1): ")
	requirePrintableASCII(t, mine, hostileEventID)
}

func TestReportReleased_HostileEventID_RendersPrintableASCII(t *testing.T) {
	var out bytes.Buffer
	reportReleased(&out, sqlite.HeldPushEvent{EventID: hostileEventID, NodeID: "N-1", OpType: "claim"},
		holdDependsPrefix+hostileEventID+" (N-1)")
	requirePrintableASCII(t, out.String(), hostileEventID)
	require.Len(t, strings.Split(strings.TrimRight(out.String(), "\n"), "\n"), 1)
}

func TestAdmitPulledEvent_HostileEventIDOnLocalFailure_RendersPrintableASCII(t *testing.T) {
	initTestApp(t)
	e := &model.SyncEvent{EventID: hostileEventID}
	err := app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		cancelled, cancel := context.WithCancel(t.Context())
		cancel()
		_, aerr := admitPulledEvent(cancelled, tx, testIngest(nil), e)
		return aerr
	})
	require.Error(t, err)
	requirePrintableASCII(t, err.Error(), hostileEventID)
}

func TestApplyBatch_HostileEventIDOnApplyFailure_RendersPrintableASCII(t *testing.T) {
	initTestApp(t)
	events := offlineEvents(t)
	events[1].EventID = hostileEventID
	err := applyBatch(t.Context(), testIngest(nil), app.store, events[1:2])
	require.Error(t, err)
	require.NotErrorIs(t, err, errCloneRefused, "this must be the apply failure, not the clock refusal")
	require.Contains(t, err.Error(), "apply ")
	requirePrintableASCII(t, err.Error(), hostileEventID)
}

func TestPageCursorAdvance_HostileEventIDs_RenderPrintableASCII(t *testing.T) {
	c := newPageCursor(transport.PullCursor{Lamport: 5, EventID: hostileEventID})
	err := c.advance([]*model.SyncEvent{{LamportClock: 3, EventID: hostileEventID + "-end"}})
	require.ErrorIs(t, err, errPageNotAfterCursor)
	requirePrintableASCII(t, err.Error(), hostileEventID)
	require.Contains(t, err.Error(), format.EventID(hostileEventID+"-end"))
}

func TestDisplayHoldReason_DependentHold_RendersIDAndKeepsStoredReason(t *testing.T) {
	raw := holdDependsPrefix + hostileEventID + " (N-é)"
	// The id has a space-free body here; the rule cuts at the first space.
	id, ok := heldDependencyID(raw)
	require.True(t, ok)
	got := displayHoldReason(raw)
	require.Contains(t, got, format.EventID(id))
	require.NotContains(t, got, "\x1b")
	b := &blocker{eventID: "ev-1", nodeID: "N-1"}
	stored := b.reason()
	require.Equal(t, holdDependsPrefix+"ev-1 (N-1)", stored, "the stored reason keeps the raw id: heldFor parses it")
	bs := newBlockers()
	bs.add(b, "")
	require.True(t, bs.heldFor(stored))
	require.Equal(t, holdDependsPrefix+format.EventID("ev-1")+" (N-1)", displayHoldReason(stored))
	require.Equal(t, stored, b.reason(), "displaying does not change the stored value")
}

func TestReportHolds_DependentHold_ShowsIDSafelyAndStoredReasonIntact(t *testing.T) {
	stored := holdDependsPrefix + "ev-é-1 (N-1)"
	h := sqlite.QuarantinedEvent{EventID: "ev-2", NodeID: "N-2", OpType: "claim", Reason: stored}
	var out bytes.Buffer
	reportHolds(&out, []sqlite.QuarantinedEvent{h})
	requirePrintableASCII(t, out.String(), "ev-2")
	require.Contains(t, out.String(), format.EventID("ev-é-1"))
	require.Equal(t, holdDependsPrefix+"ev-é-1 (N-1)", h.Reason, "the held row is not rewritten")
}

func TestQuarantineTable_DependentHoldReason_ShowsIDSafely(t *testing.T) {
	q := service.QuarantinedEvent{EventID: "ev-2", NodeID: "N-2", OpType: "claim", Source: "push",
		Reason: holdDependsPrefix + "ev-é-1 (N-1)"}
	var out bytes.Buffer
	require.NoError(t, printQuarantineTable(&out, []service.QuarantinedEvent{q}))
	requirePrintableASCII(t, out.String(), "ev-2")
	require.Contains(t, out.String(), format.EventID("ev-é-1"))
}

func TestCheckHeldPushEvents_DependentHoldReason_ShowsIDSafely(t *testing.T) {
	initTestApp(t)
	require.NoError(t, app.store.WithTx(t.Context(), func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(t.Context(), tx, sqlite.QuarantinedEvent{
			EventID: "ev-2", Source: "push", RawEvent: `{"node_id":"TEST-2","op_type":"claim"}`,
			Reason: holdDependsPrefix + "ev-é-1 (TEST-1)", FirstSeen: "2026-03-14T10:00:00Z", LastAttempt: "2026-03-14T10:00:00Z",
		})
	}))
	ok, detail := checkHeldPushEvents(t.Context(), app.store)
	require.False(t, ok)
	require.Contains(t, detail, format.EventID("ev-é-1"))
	require.NotContains(t, detail, "é")
}

func TestCheckPulledEvent_HostileFutureStampedID_WarnsPrintableASCII(t *testing.T) {
	var warn bytes.Buffer
	in := testIngest(&warn)
	future := &model.SyncEvent{EventID: hostileEventID, ProjectPrefix: "TEST", AuthorID: "peer", AuthorMachineHash: "0123456789abcdef",
		LamportClock: 1, NodeID: "TEST-1", OpType: model.OpCreateNode, WallClockTS: in.now().Add(72 * time.Hour).UnixMilli(),
		VectorClock: map[string]int64{}, Payload: json.RawMessage(`{"title":"t"}`)}
	require.NoError(t, checkPulledEvent(in, future, 0))
	requirePrintableASCII(t, warn.String(), hostileEventID)
}
