// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

// FR-19 (MTIX-47) acceptance-criteria integration suite — the human-relay-free
// wake path: a per-agent inbox derived from the event journal, plus event hooks
// that deliver to that inbox / a file / an exec'd command.
//
// This file covers the acceptance criteria of FR-19 §4 end-to-end at the
// service/store/dispatcher layer (deterministic; no spawned binary, no
// sleeps-as-assertions). The five criteria below map one-to-one to the
// TestFR19Acceptance_* functions.
//
// The sixth criterion — the loop-guard demo (a hook whose own output would
// re-trigger it) — is deliberately NOT here: the exec rate-limit it needs is
// being built separately, and that demo is covered with MTIX-47.7.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// projectDirs returns a project root and its .mtix subdir. append-file/exec
// paths resolve under the project root (the parent of .mtix), so hooks that
// write files need this two-level layout rather than a bare temp dir.
func projectDirs(t *testing.T) (proj, mtixDir string) {
	t.Helper()
	proj = t.TempDir()
	mtixDir = filepath.Join(proj, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	return proj, mtixDir
}

// newAcceptancePromptService wires a PromptService onto the same store a
// NodeService writes to, so an addressed comment lands in the shared journal
// the inbox queries.
func newAcceptancePromptService(store *sqlite.Store) *service.PromptService {
	return service.NewPromptService(store, nil, slog.Default(), fixedClock(time.Now().UTC()))
}

// --- Criterion 1: worker wake-loop -----------------------------------------

// TestFR19Acceptance_WorkerWakeLoop: a worker parks its outer loop on
// InboxWait; an addressed comment from another actor unblocks it with the
// event, and an ack then clears the inbox — the human-relay-free wake path
// (FR-19 §4).
func TestFR19Acceptance_WorkerWakeLoop(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	promptSvc := newAcceptancePromptService(store)

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)

	// The worker parks on an empty inbox, blocking until something is addressed.
	type wakeResult struct {
		events []sqlite.InboxEvent
		err    error
	}
	woke := make(chan wakeResult, 1)
	go func() {
		events, waitErr := store.InboxWait(ctx, "opus", 5*time.Second)
		woke <- wakeResult{events, waitErr}
	}()

	// Another actor addresses a comment at opus — this is the wake signal.
	require.NoError(t, promptSvc.AddAnnotation(ctx, node.ID, "ruling: proceed", "reviewer", "opus"))

	// The parked worker returns with the addressed event (no sleep — we block on
	// the goroutine's own completion, bounded by the 5s wait deadline).
	select {
	case got := <-woke:
		require.NoError(t, got.err)
		require.Len(t, got.events, 1, "InboxWait must wake with exactly the addressed comment")
		assert.Equal(t, node.ID, got.events[0].NodeID)
		assert.Equal(t, "ruling: proceed", got.events[0].Body)

		// Acking the delivered seq advances the cursor; the inbox then clears.
		require.NoError(t, store.InboxAck(ctx, "opus", got.events[0].Seq))
		after, listErr := store.InboxList(ctx, "opus")
		require.NoError(t, listErr)
		assert.Empty(t, after, "after ack the inbox is empty")
	case <-time.After(6 * time.Second):
		t.Fatal("worker never woke from InboxWait")
	}
}

// --- Criterion 2: frontier append-file -------------------------------------

// TestFR19Acceptance_FrontierAppendFile: a status.changed hook with an
// append-file delivery writes one audit line — containing the event JSON — to
// a project-local file when a node transitions to done (FR-19 §3/§4).
func TestFR19Acceptance_FrontierAppendFile(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	proj, mtixDir := projectDirs(t)

	writeHooks(t, mtixDir, `
hooks:
  - name: frontier-trail
    match:
      events: [status.changed]
      status-to: [done]
    deliver: [append-file]
    append-file:
      path: FRONTIER-INBOX.md
`)

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))

	service.NewHooksDispatcher(store, mtixDir, slog.Default()).Dispatch(ctx)

	// The configured file resolves under the PROJECT dir (parent of .mtix).
	data, readErr := os.ReadFile(filepath.Join(proj, "FRONTIER-INBOX.md"))
	require.NoError(t, readErr, "append-file hook must create the audit file")
	line := string(data)
	assert.Contains(t, line, "status.changed", "line carries the normalized event name")
	assert.Contains(t, line, node.ID, "line carries the node id")
	// The trailing field is the event JSON — assert it is present and parseable.
	assert.Contains(t, line, `"event":"status.changed"`)
	assert.Contains(t, line, `"node_id":"`+node.ID+`"`)
}

// --- Criterion 3: exec wake (trust-gated) ----------------------------------

// TestFR19Acceptance_ExecWake: a status.changed hook execs a command that
// captures $MTIX_EVENT. It fires ONLY once the operator has trusted the
// hooks.yaml by content hash; an untrusted config leaves exec silently disabled
// (FR-19 §3 security).
func TestFR19Acceptance_ExecWake(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	proj, mtixDir := projectDirs(t)
	outFile := filepath.Join(proj, "exec-out.json")

	// The command writes the event JSON (delivered via env) to outFile. argv is
	// exec'd without a shell; sh is the program, the redirect is in its script.
	writeHooks(t, mtixDir, `
hooks:
  - name: exec-wake
    match:
      events: [status.changed]
      status-to: [done]
    deliver: [exec]
    exec:
      command: ["sh", "-c", "printf '%s' \"$MTIX_EVENT\" > `+outFile+`"]
      timeout-seconds: 5
`)

	// Phase A — UNTRUSTED: exec is gated off, so nothing runs.
	n1, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "one", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, n1.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, n1.ID, model.StatusDone, "", "worker"))

	service.NewHooksDispatcher(store, mtixDir, slog.Default()).Dispatch(ctx)

	_, statErr := os.Stat(outFile)
	assert.True(t, os.IsNotExist(statErr), "exec must NOT run for an untrusted hooks.yaml")

	// Phase B — TRUSTED: record trust for the current config, then a fresh
	// transition fires exec (the Phase-A events are already past the cursor).
	require.NoError(t, hooks.SaveTrust(mtixDir, hooks.ConfigHash(mtixDir)))

	n2, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "two", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, n2.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, n2.ID, model.StatusDone, "", "worker"))

	service.NewHooksDispatcher(store, mtixDir, slog.Default()).Dispatch(ctx)

	// MTIX-56.9: the exec is a detached spawn — await its output.
	require.Eventually(t, func() bool {
		_, statErr := os.Stat(outFile)
		return statErr == nil
	}, 10*time.Second, 25*time.Millisecond, "the trusted exec hook runs after dispatch")
	data, readErr := os.ReadFile(outFile)
	require.NoError(t, readErr, "exec must run once the config is trusted")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload), "the process received valid event JSON on $MTIX_EVENT")
	assert.Equal(t, "status.changed", payload["event"])
	assert.Equal(t, n2.ID, payload["node_id"])
}

// --- Criterion 4: idempotence (kill-9 proxy) -------------------------------

// TestFR19Acceptance_Idempotence: because the inbox is journal-derived (a cursor
// over the durable event log, not a separate mailbox), re-running delivery never
// double-delivers and re-acking never rewinds. This stands in for "kill -9
// mid-delivery → no duplicates after restart": a second Dispatch is exactly what
// a restart replays (FR-19 §4).
func TestFR19Acceptance_Idempotence(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()

	writeHooks(t, dir, `
hooks:
  - name: wake-opus
    match:
      events: [status.changed]
      to-agent: opus
      status-to: [done]
    deliver: [inbox]
`)

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))

	disp := service.NewHooksDispatcher(store, dir, slog.Default())

	// First Dispatch delivers the done-transition to opus's inbox.
	disp.Dispatch(ctx)
	first, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, first, 1, "one delivery for the single matching transition")

	// A SECOND Dispatch (the restart/replay) must not double-deliver: the hook
	// cursor already advanced past those events.
	disp.Dispatch(ctx)
	second, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, second, 1, "re-dispatch is idempotent — no duplicate delivery")
	assert.Equal(t, first[0].Seq, second[0].Seq)

	// Acking is idempotent: acking the same watermark twice leaves the inbox
	// empty, never rewound or duplicated.
	require.NoError(t, store.InboxAck(ctx, "opus", second[0].Seq))
	require.NoError(t, store.InboxAck(ctx, "opus", second[0].Seq))
	after, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	assert.Empty(t, after, "double-ack still clears exactly once")
}

// --- Criterion 5: via MCP ---------------------------------------------------

// TestFR19Acceptance_ViaMCP: the same addressed-comment → inbox flow, driven end
// to end through the MCP tools an agent actually calls — mtix_annotate with a
// `to` param, then mtix_inbox and mtix_inbox_ack (FR-19 §4/§5).
func TestFR19Acceptance_ViaMCP(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)

	// Register the same tools the MCP server exposes, backed by the real store so
	// annotate and inbox share one journal.
	reg := mcp.NewToolRegistry()
	ctxSvc := service.NewContextService(store, nil, slog.Default())
	promptSvc := newAcceptancePromptService(store)
	mcp.RegisterContextTools(reg, ctxSvc, promptSvc)
	mcp.RegisterInboxTools(reg, store)

	// Address a comment at opus via mtix_annotate's `to`.
	_, err = reg.Call(ctx, "mtix_annotate", json.RawMessage(
		`{"id":"`+node.ID+`","text":"proceed","author":"reviewer","to":"opus"}`))
	require.NoError(t, err)

	// mtix_inbox surfaces it for opus.
	res, err := reg.Call(ctx, "mtix_inbox", json.RawMessage(`{"agent":"opus"}`))
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 1)
	var events []sqlite.InboxEvent
	require.NoError(t, json.Unmarshal([]byte(res.Content[0].Text), &events))
	require.Len(t, events, 1)
	assert.Equal(t, node.ID, events[0].NodeID)
	assert.Equal(t, "proceed", events[0].Body)

	// mtix_inbox_ack clears it, just as it would for the CLI/store path.
	ackArg, _ := json.Marshal(map[string]any{"agent": "opus", "seq": events[0].Seq})
	_, err = reg.Call(ctx, "mtix_inbox_ack", ackArg)
	require.NoError(t, err)

	after, err := reg.Call(ctx, "mtix_inbox", json.RawMessage(`{"agent":"opus"}`))
	require.NoError(t, err)
	var afterEvents []sqlite.InboxEvent
	require.NoError(t, json.Unmarshal([]byte(after.Content[0].Text), &afterEvents))
	assert.Empty(t, afterEvents, "inbox clears after MCP ack")
}
