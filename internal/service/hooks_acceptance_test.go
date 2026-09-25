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
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
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
	// The detached spawn can write after the stat above; the audit log is
	// written during Dispatch, so this check is exact (MTIX-107.23).
	assert.Equal(t, []string{sqlite.OutcomeSkippedUntrusted}, execOutcomes(t, store, "exec-wake", n1.ID),
		"n1's exec delivery is recorded as skipped for the untrusted hooks.yaml")

	// Phase B — TRUSTED: record trust for the current config, then a fresh
	// transition fires exec (the Phase-A events are already past the cursor).
	require.NoError(t, hooks.SaveTrust(mtixDir, hooks.ConfigHash(mtixDir)))

	n2, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "two", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, n2.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, n2.ID, model.StatusDone, "", "worker"))

	service.NewHooksDispatcher(store, mtixDir, slog.Default()).Dispatch(ctx)

	// MTIX-56.9: the exec is a detached spawn — await its complete output.
	payload := awaitEventJSON(t, func() ([]byte, error) { return os.ReadFile(outFile) })
	assert.Equal(t, "status.changed", payload["event"])
	assert.Equal(t, n2.ID, payload["node_id"])
}

// execOutcomes returns the audit-log outcomes of hook's exec deliveries for
// nodeID. The dispatcher writes them synchronously during Dispatch (FR-19.7),
// so unlike the exec's own output they need no waiting.
func execOutcomes(t *testing.T, store *sqlite.Store, hook, nodeID string) []string {
	t.Helper()
	entries, err := store.ReadHookLog(context.Background(), 1000)
	require.NoError(t, err)
	var outcomes []string
	for _, e := range entries {
		if e.Hook == hook && e.NodeID == nodeID && e.Adapter == hooks.AdapterExec {
			outcomes = append(outcomes, e.Outcome)
		}
	}
	return outcomes
}

// awaitEventJSON polls read until it returns the exec hook's complete event
// JSON and returns the decoded payload. The exec is a detached spawn
// (MTIX-56.9), and the hook's shell creates (truncates) its output file before
// printf writes to it, so a file that exists may still be empty or partly
// written. Only a read that parses counts as delivery (MTIX-107.23); anything
// less keeps polling until the deadline, whose failure reports the last read.
func awaitEventJSON(t *testing.T, read func() ([]byte, error)) map[string]any {
	t.Helper()
	var payload map[string]any
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		data, err := read()
		if !assert.NoError(c, err, "exec must run once the config is trusted") {
			return
		}
		var got map[string]any
		if !assert.NoError(c, json.Unmarshal(data, &got), "the process received complete event JSON on $MTIX_EVENT") {
			return
		}
		payload = got
	}, 10*time.Second, 25*time.Millisecond, "the trusted exec hook delivers its complete event JSON")
	return payload
}

// scriptedRead is one canned result of an output-file read.
type scriptedRead struct {
	data string
	err  error
}

// TestAwaitEventJSON_IncompleteReads_PollsUntilJSONParses: a read that is not
// yet complete event JSON — no file yet, the empty file a shell redirect
// creates before its command writes, a partly written object — keeps the wait
// polling; the first read that parses is the payload returned (MTIX-107.23).
func TestAwaitEventJSON_IncompleteReads_PollsUntilJSONParses(t *testing.T) {
	complete := scriptedRead{data: `{"event":"status.changed","node_id":"PROJ-2"}`}
	missing := scriptedRead{err: fs.ErrNotExist}
	tests := []struct {
		name  string
		reads []scriptedRead // the last read is the complete event
	}{
		{"complete on the first read", []scriptedRead{complete}},
		{"output not created yet", []scriptedRead{missing, complete}},
		{"created but still empty", []scriptedRead{missing, {data: ""}, complete}},
		{"partly written", []scriptedRead{{data: ""}, {data: `{"event":"status.chan`}, complete}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0 // polls run one at a time, each after the previous finished
			read := func() ([]byte, error) {
				r := tt.reads[min(calls, len(tt.reads)-1)]
				calls++
				return []byte(r.data), r.err
			}

			payload := awaitEventJSON(t, read)

			assert.Equal(t, map[string]any{"event": "status.changed", "node_id": "PROJ-2"}, payload)
			assert.Equal(t, len(tt.reads), calls, "the wait stops at the first read that parses, and not before")
		})
	}
}

// TestAwaitEventJSON_ExecOutputCreatedBeforeWritten_WaitsForCompleteJSON
// reproduces the MTIX-107.23 flake on the real exec path without timing. The
// hook creates its output file, as the redirect in the ExecWake hook does, and
// then parks on a FIFO gate before printf writes the event. The gate opens
// only inside a read that returns the created-but-empty file, so a wait that
// takes "the file exists" for delivery fails on every run, and a wait that
// polls until the JSON parses passes.
func TestAwaitEventJSON_ExecOutputCreatedBeforeWritten_WaitsForCompleteJSON(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	proj, mtixDir := projectDirs(t)
	outFile := filepath.Join(proj, "exec-out.json")
	gate := &execGate{path: filepath.Join(proj, "exec-gate")}

	// mkfifo runs first, so the gate exists whenever outFile does.
	writeHooks(t, mtixDir, `
hooks:
  - name: exec-gated
    match:
      events: [status.changed]
      status-to: [done]
    deliver: [exec]
    exec:
      command: ["sh", "-c", "mkfifo '`+gate.path+`' && : > '`+outFile+`' && read _ < '`+gate.path+`'; printf '%s' \"$MTIX_EVENT\" > '`+outFile+`'"]
      timeout-seconds: 5
`)
	require.NoError(t, hooks.SaveTrust(mtixDir, hooks.ConfigHash(mtixDir)))
	t.Cleanup(func() { gate.releaseIfParked(t) })

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "gated", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))
	service.NewHooksDispatcher(store, mtixDir, slog.Default()).Dispatch(ctx)

	payload := awaitEventJSON(t, gate.reader(outFile))

	assert.True(t, gate.handedEmpty.Load(), "the wait was first handed the created-but-empty output")
	assert.Equal(t, "status.changed", payload["event"])
	assert.Equal(t, node.ID, payload["node_id"])
}

// execGate holds an exec hook parked on `read _ < path` (a FIFO) between
// creating its output file and writing it (MTIX-107.23).
type execGate struct {
	path        string
	opened      atomic.Bool // the parked hook was released
	handedEmpty atomic.Bool // the read that released it returned an empty file
}

// reader returns a read of outFile that releases the hook the first time it
// finds outFile while the gate is shut. That read returns what it found — the
// file the hook created and has not yet written — so the wait under test is
// always handed exactly the state that flaked.
func (g *execGate) reader(outFile string) func() ([]byte, error) {
	return func() ([]byte, error) {
		data, err := os.ReadFile(outFile)
		if err != nil || g.opened.Load() {
			return data, err
		}
		if openErr := g.open(); openErr != nil {
			return nil, openErr // the shell has not opened the FIFO yet; retry on the next poll
		}
		g.handedEmpty.Store(len(data) == 0)
		return data, nil
	}
}

// open writes one line to the FIFO, which ends the hook's `read`. The
// non-blocking open fails while no process has the FIFO open for reading,
// that is, before the shell reaches the `read`.
func (g *execGate) open() error {
	f, err := os.OpenFile(g.path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return fmt.Errorf("open exec gate %s: %w", g.path, err)
	}
	if _, err := f.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write exec gate %s: %w", g.path, errors.Join(err, f.Close()))
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close exec gate %s: %w", g.path, err)
	}
	g.opened.Store(true)
	return nil
}

// releaseIfParked frees a hook still parked on the gate when the test failed
// before its wait opened it. A fast failure can come before the shell reaches
// the gate, and the exec adapter's own timeout dies with the test binary, so
// it retries until the shell is there to release: a failed run leaves no
// blocked shell behind. A passing run opened the gate and returns at once.
func (g *execGate) releaseIfParked(t *testing.T) {
	t.Helper()
	if g.opened.Load() {
		return
	}
	assert.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.NoError(c, g.open())
	}, 5*time.Second, 10*time.Millisecond, "release the exec hook still parked on the gate")
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
