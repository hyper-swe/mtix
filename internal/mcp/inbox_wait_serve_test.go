// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// serveInboxInput models owned stdio: queued frames drain before EOF, and Close
// also interrupts an idle Read. No sender or scanner goroutine is left behind.
type serveInboxInput struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    bytes.Buffer
	closed bool
	err    error
}

func newServeInboxInput() *serveInboxInput {
	in := &serveInboxInput{}
	in.cond = sync.NewCond(&in.mu)
	return in
}

func (in *serveInboxInput) Read(p []byte) (int, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	for in.buf.Len() == 0 && !in.closed {
		in.cond.Wait()
	}
	if in.buf.Len() == 0 {
		if in.err != nil {
			return 0, in.err
		}
		return 0, io.EOF
	}
	return in.buf.Read(p)
}

func (in *serveInboxInput) fail(err error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.err = err
	in.closed = true
	in.cond.Broadcast()
}

func (in *serveInboxInput) Close() error {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.closed = true
	in.cond.Broadcast()
	return nil
}

func (in *serveInboxInput) send(t *testing.T, id any, method string, params any) {
	t.Helper()
	frame := map[string]any{"jsonrpc": "2.0", "method": method}
	if id != nil {
		frame["id"] = id
	}
	if params != nil {
		frame["params"] = params
	}
	data, err := json.Marshal(frame)
	require.NoError(t, err)
	in.mu.Lock()
	defer in.mu.Unlock()
	require.False(t, in.closed, "sending on a closed test connection")
	_, err = in.buf.Write(append(data, '\n'))
	require.NoError(t, err)
	in.cond.Signal()
}

type serveInboxOutput struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	frames chan []byte
}

func (out *serveInboxOutput) Write(p []byte) (int, error) {
	out.mu.Lock()
	defer out.mu.Unlock()
	n, err := out.buf.Write(p)
	for {
		data := out.buf.Bytes()
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			break
		}
		out.frames <- bytes.Clone(data[:end])
		out.buf.Next(end + 1)
	}
	return n, err
}

type serveInboxCall struct {
	agent   string
	timeout time.Duration
	ctx     context.Context
	event   chan struct{}
	exited  chan struct{}
}

type serveInboxBackend struct{ started chan *serveInboxCall }

func (*serveInboxBackend) InboxList(context.Context, string) ([]sqlite.InboxEvent, error) {
	return []sqlite.InboxEvent{{Seq: 9, Body: "ordinary inbox response"}}, nil
}
func (*serveInboxBackend) InboxAck(context.Context, string, int64) error { return nil }
func (b *serveInboxBackend) InboxWait(ctx context.Context, agent string, timeout time.Duration) ([]sqlite.InboxEvent, error) {
	call := &serveInboxCall{agent: agent, timeout: timeout, ctx: ctx, event: make(chan struct{}), exited: make(chan struct{})}
	defer close(call.exited)
	b.started <- call
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-call.event:
		return []sqlite.InboxEvent{{Seq: 7, Body: "delivered event"}}, nil
	}
}

type serveInboxHarness struct {
	in       *serveInboxInput
	out      *serveInboxOutput
	backend  *serveInboxBackend
	server   *Server
	cancel   context.CancelFunc
	done     chan error
	joined   bool
	observed []*serveInboxCall
}

func newServeInboxHarness(t *testing.T) *serveInboxHarness {
	t.Helper()
	return newServeInboxHarnessWithWriter(t, nil)
}

func newServeInboxHarnessWithWriter(t *testing.T, writer io.Writer) *serveInboxHarness {
	t.Helper()
	h := &serveInboxHarness{in: newServeInboxInput(), out: &serveInboxOutput{frames: make(chan []byte, 1024)}, backend: &serveInboxBackend{started: make(chan *serveInboxCall, 256)}, done: make(chan error, 1)}
	if writer == nil {
		writer = h.out
	}
	h.server = NewServer(h.in, writer, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	RegisterInboxTools(h.server.Registry(), testInboxService(h.backend))
	ctx, cancel := context.WithCancel(t.Context())
	h.cancel = cancel
	go func() { h.done <- h.server.Serve(ctx) }()
	t.Cleanup(func() {
		h.cancel()
		assert.NoError(t, h.in.Close())
		if !h.joined {
			select {
			case <-h.done:
				h.joined = true
			case <-time.After(2 * time.Second):
				t.Error("Serve goroutine did not join during cleanup")
			}
		}
		for _, call := range h.observed {
			select {
			case <-call.exited:
			case <-time.After(time.Second):
				t.Error("observed wait worker did not exit during cleanup")
			}
		}
		for {
			select {
			case call := <-h.backend.started:
				select {
				case <-call.exited:
				case <-time.After(time.Second):
					t.Error("admitted wait worker did not exit during cleanup")
				}
			default:
				return
			}
		}
	})
	return h
}

func (h *serveInboxHarness) wait(t *testing.T, id any, args map[string]any) *serveInboxCall {
	t.Helper()
	h.in.send(t, id, MethodToolsCall, map[string]any{"name": "mtix_inbox_wait", "arguments": args})
	select {
	case call := <-h.backend.started:
		h.observed = append(h.observed, call)
		return call
	case <-time.After(time.Second):
		t.Fatal("inbox wait was not dispatched")
		return nil
	}
}

func (h *serveInboxHarness) response(t *testing.T, id any) Response {
	t.Helper()
	want, err := json.Marshal(id)
	require.NoError(t, err)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		select {
		case frame := <-h.out.frames:
			var resp Response
			require.NoError(t, json.Unmarshal(frame, &resp), "response/notification writes must form complete JSON frames")
			if bytes.Equal(resp.ID, want) {
				return resp
			}
			require.Nil(t, resp.ID, "unexpected response while waiting for %s: %s", want, frame)
		case <-deadline.C:
			t.Fatalf("no response for request %s while inbox wait is active", want)
			return Response{}
		}
	}
}

func requireServeInboxExited(t *testing.T, call *serveInboxCall) {
	t.Helper()
	select {
	case <-call.exited:
	case <-time.After(time.Second):
		t.Fatal("targeted wait did not release its worker")
	}
}

func (h *serveInboxHarness) finish(t *testing.T, want error) {
	t.Helper()
	select {
	case err := <-h.done:
		h.joined = true
		if want == nil {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, want)
		}
	case <-time.After(time.Second):
		t.Fatal("Serve did not stop and join its wait workers")
	}
}

func TestInboxWaitServe_ZeroCancellationBeforeEventKeepsConnectionResponsive(t *testing.T) {
	for _, id := range []any{2, "wait-2", json.RawMessage(`"wait\u002d2"`)} {
		t.Run(string(mustServeInboxJSON(t, id)), func(t *testing.T) {
			h := newServeInboxHarness(t)
			call := h.wait(t, id, map[string]any{"agent": "worker", "timeout_seconds": 0})
			require.Zero(t, call.timeout)
			cancelID := id
			if _, escaped := id.(json.RawMessage); escaped {
				cancelID = "wait-2"
			}
			h.in.send(t, nil, "notifications/cancelled", map[string]any{"requestId": cancelID, "reason": "cancel before event"})
			requireServeInboxExited(t, call)
			require.ErrorIs(t, call.ctx.Err(), context.Canceled)
			h.in.send(t, 3, MethodPing, nil)
			require.Nil(t, h.response(t, 3).Error, "cancelled request must not emit a late response")
			h.in.send(t, 4, MethodToolsCall, map[string]any{"name": "mtix_inbox", "arguments": map[string]any{"agent": "worker"}})
			resp := h.response(t, 4)
			require.Nil(t, resp.Error)
			data, err := json.Marshal(resp.Result)
			require.NoError(t, err)
			assert.Contains(t, string(data), "ordinary inbox response")
			require.NoError(t, h.in.Close())
			h.finish(t, nil)
			assert.Empty(t, h.out.frames, "cancelled wait must never produce a response")
		})
	}
}

func mustServeInboxJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func TestInboxWaitServe_ZeroDoesNotBlockPingOrdinaryToolOrNotifications(t *testing.T) {
	h := newServeInboxHarness(t)
	call := h.wait(t, "pending", map[string]any{"agent": "worker", "timeout_seconds": 0})
	h.in.send(t, 3, MethodPing, nil)
	require.Nil(t, h.response(t, 3).Error)
	h.in.send(t, 4, MethodToolsCall, map[string]any{"name": "mtix_inbox", "arguments": map[string]any{"agent": "worker"}})
	require.Nil(t, h.response(t, 4).Error)
	notifyDone := make(chan error, 1)
	go func() {
		notifyDone <- h.server.SendNotification("notifications/test", map[string]any{"body": "concurrent notification"})
	}()
	joined := false
	t.Cleanup(func() {
		if !joined {
			select {
			case err := <-notifyDone:
				assert.NoError(t, err)
			case <-time.After(time.Second):
				t.Error("notification sender did not join")
			}
		}
	})
	close(call.event)
	resp := h.response(t, "pending")
	require.Nil(t, resp.Error)
	select {
	case err := <-notifyDone:
		joined = true
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("notification sender did not finish")
	}
	requireServeInboxExited(t, call)
	require.NoError(t, h.in.Close())
	h.finish(t, nil)
	for len(h.out.frames) > 0 {
		var notification Notification
		require.NoError(t, json.Unmarshal(<-h.out.frames, &notification))
		assert.Equal(t, "notifications/test", notification.Method)
	}
}

func TestInboxWaitServe_RequestIDsTargetOnlyMatchingWait(t *testing.T) {
	h := newServeInboxHarness(t)
	number := h.wait(t, 7, map[string]any{"agent": "numeric", "timeout_seconds": 0})
	text := h.wait(t, "7", map[string]any{"agent": "string", "timeout_seconds": 0})
	h.in.send(t, nil, "notifications/cancelled", map[string]any{"requestId": 7})
	requireServeInboxExited(t, number)
	assert.NoError(t, text.ctx.Err(), "string ID must not collide with numeric ID")
	for _, params := range []any{map[string]any{"requestId": "unknown"}, map[string]any{"requestId": true}, map[string]any{}, "malformed"} {
		h.in.send(t, nil, "notifications/cancelled", params)
	}
	h.in.send(t, 8, MethodPing, nil)
	require.Nil(t, h.response(t, 8).Error)
	assert.NoError(t, text.ctx.Err())
	close(text.event)
	require.Nil(t, h.response(t, "7").Error)
	requireServeInboxExited(t, text)
	// A completed/cancelled ID can be reused without stale cleanup cancelling it.
	reused := h.wait(t, 7, map[string]any{"agent": "reused", "timeout_seconds": 0})
	close(reused.event)
	require.Nil(t, h.response(t, 7).Error)
	requireServeInboxExited(t, reused)
	require.NoError(t, h.in.Close())
	h.finish(t, nil)
}

func TestInboxWaitServe_EOFCancelsAndJoinsActiveWait(t *testing.T) {
	h := newServeInboxHarness(t)
	call := h.wait(t, 2, map[string]any{"agent": "worker", "timeout_seconds": 0})
	require.NoError(t, h.in.Close())
	h.finish(t, nil)
	requireServeInboxExited(t, call)
	require.ErrorIs(t, call.ctx.Err(), context.Canceled)
}

func TestInboxWaitServe_ReadErrorCancelsAndJoinsActiveWait(t *testing.T) {
	h := newServeInboxHarness(t)
	call := h.wait(t, 2, map[string]any{"agent": "worker", "timeout_seconds": 0})
	readFailure := errors.New("owned stdin read failure")
	h.in.fail(readFailure)
	h.finish(t, readFailure)
	requireServeInboxExited(t, call)
	require.ErrorIs(t, call.ctx.Err(), context.Canceled)
}

func TestInboxWaitServe_DuplicateActiveIDPreservesOriginalCancellationOwner(t *testing.T) {
	for _, id := range []any{2, "wait-2"} {
		t.Run(string(mustServeInboxJSON(t, id)), func(t *testing.T) {
			h := newServeInboxHarness(t)
			original := h.wait(t, id, map[string]any{"agent": "original", "timeout_seconds": 0})
			h.in.send(t, id, MethodToolsCall, map[string]any{"name": "mtix_inbox_wait", "arguments": map[string]any{"agent": "duplicate", "timeout_seconds": 0}})
			refused := h.response(t, id)
			require.NotNil(t, refused.Error, "a live request ID must not be overwritten or dispatched twice")
			assert.Equal(t, ErrCodeInvalidRequest, refused.Error.Code)
			assert.NoError(t, original.ctx.Err(), "duplicate rejection must preserve the original wait")
			h.in.send(t, 3, MethodPing, nil)
			require.Nil(t, h.response(t, 3).Error)
			assert.Empty(t, h.backend.started, "duplicate must not acquire a second backend worker")
			h.in.send(t, nil, "notifications/cancelled", map[string]any{"requestId": id})
			requireServeInboxExited(t, original)
			require.ErrorIs(t, original.ctx.Err(), context.Canceled)
			h.in.send(t, 4, MethodPing, nil)
			require.Nil(t, h.response(t, 4).Error, "cancelled original must not emit a response")
			require.NoError(t, h.in.Close())
			h.finish(t, nil)
			assert.Empty(t, h.out.frames)
		})
	}
}

func TestInboxWaitServe_SessionCancellationInterruptsIdleReaderAndJoinsWait(t *testing.T) {
	h := newServeInboxHarness(t)
	call := h.wait(t, 2, map[string]any{"agent": "worker", "timeout_seconds": 0})
	close(call.event)
	require.Nil(t, h.response(t, 2).Error)
	requireServeInboxExited(t, call)
	// Leave stdin open and idle: cancellation must interrupt the owned Read.
	h.cancel()
	h.finish(t, context.Canceled)
}

func TestInboxWaitServe_SessionCancellationJoinsActiveWait(t *testing.T) {
	h := newServeInboxHarness(t)
	call := h.wait(t, 2, map[string]any{"agent": "worker", "timeout_seconds": 0})
	h.cancel()
	h.finish(t, context.Canceled)
	requireServeInboxExited(t, call)
	require.ErrorIs(t, call.ctx.Err(), context.Canceled)
}

func TestInboxWaitServe_FiniteDefaultAndCappedContracts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seconds *int
		want    time.Duration
	}{
		{"omitted", nil, 60 * time.Second}, {"positive", serveInboxInt(1), time.Second}, {"capped", serveInboxInt(61), 60 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newServeInboxHarness(t)
			args := map[string]any{"agent": "worker"}
			if tc.seconds != nil {
				args["timeout_seconds"] = *tc.seconds
			}
			call := h.wait(t, 2, args)
			assert.Equal(t, tc.want, call.timeout)
			close(call.event)
			resp := h.response(t, 2)
			require.Nil(t, resp.Error)
			data, err := json.Marshal(resp.Result)
			require.NoError(t, err)
			assert.Contains(t, string(data), "delivered event")
			requireServeInboxExited(t, call)
			require.NoError(t, h.in.Close())
			h.finish(t, nil)
		})
	}
}

func serveInboxInt(n int) *int { return &n }

func TestInboxWaitServe_AdmissionBoundKeepsPingResponsiveAndJoinsWorkers(t *testing.T) {
	h := newServeInboxHarness(t)
	// This is a resource ceiling rather than a prescribed internal pool size.
	// A smaller limit is valid; accepting all 128 indefinite waits is not.
	for id := 1; id <= 128; id++ {
		h.in.send(t, id, MethodToolsCall, map[string]any{"name": "mtix_inbox_wait", "arguments": map[string]any{"agent": "worker", "timeout_seconds": 0}})
	}
	h.in.send(t, 129, MethodPing, nil)
	h.requireAdmissionRefusals(t)
	h.collectAdmitted()
	require.NotEmpty(t, h.observed)
	require.LessOrEqual(t, len(h.observed), 64, "indefinite wait resources must be bounded")
	require.NoError(t, h.in.Close())
	h.finish(t, nil)
	for _, call := range h.observed {
		requireServeInboxExited(t, call)
	}
}

func (h *serveInboxHarness) requireAdmissionRefusals(t *testing.T) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	refused := 0
	for {
		select {
		case frame := <-h.out.frames:
			var resp Response
			require.NoError(t, json.Unmarshal(frame, &resp))
			if string(resp.ID) == "129" {
				require.Nil(t, resp.Error)
				require.GreaterOrEqual(t, refused, 64, "admission must bound indefinite workers to at most 64")
				return
			}
			if resp.Error == nil {
				var result ToolsCallResult
				require.NoError(t, json.Unmarshal(mustServeInboxJSON(t, resp.Result), &result))
				require.True(t, result.IsError, "excess wait must be refused explicitly")
			}
			refused++
		case <-deadline.C:
			t.Fatal("admission overload prevented ping response")
		}
	}
}

func (h *serveInboxHarness) collectAdmitted() {
	for {
		select {
		case call := <-h.backend.started:
			h.observed = append(h.observed, call)
		default:
			return
		}
	}
}

func TestInboxWaitServe_CompletionCancellationRaceProducesAtMostOneResponse(t *testing.T) {
	h := newServeInboxHarness(t)
	seen := make(map[string]int)
	for id := 1; id <= 20; id++ {
		call := h.wait(t, id, map[string]any{"agent": "worker", "timeout_seconds": 0})
		h.raceCompletionAndCancellation(t, call, id)
		requireServeInboxExited(t, call)
		h.in.send(t, 1000+id, MethodPing, nil)
		h.requireRacePing(t, 1000+id, seen)
	}
	require.NoError(t, h.in.Close())
	h.finish(t, nil)
	for len(h.out.frames) > 0 {
		recordServeInboxResponse(t, <-h.out.frames, seen)
	}
}

func (h *serveInboxHarness) raceCompletionAndCancellation(t *testing.T, call *serveInboxCall, id int) {
	t.Helper()
	// Send and event completion race without sleep-based ordering.
	var racers sync.WaitGroup
	racers.Add(2)
	go func() { defer racers.Done(); close(call.event) }()
	// Build the frame on the test goroutine: worker code never calls Fatal.
	data := mustServeInboxJSON(t, map[string]any{"jsonrpc": "2.0", "method": "notifications/cancelled", "params": map[string]any{"requestId": id}})
	writeDone := make(chan error, 1)
	go func() {
		defer racers.Done()
		h.in.mu.Lock()
		defer h.in.mu.Unlock()
		_, err := h.in.buf.Write(append(data, '\n'))
		writeDone <- err
		h.in.cond.Signal()
	}()
	racers.Wait()
	require.NoError(t, <-writeDone)
}

func (h *serveInboxHarness) requireRacePing(t *testing.T, id int, seen map[string]int) {
	t.Helper()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	want := string(mustServeInboxJSON(t, id))
	for {
		select {
		case frame := <-h.out.frames:
			if recordServeInboxResponse(t, frame, seen) == want {
				return
			}
		case <-deadline.C:
			t.Fatal("completion/cancellation race stranded the connection")
		}
	}
}

func recordServeInboxResponse(t *testing.T, frame []byte, seen map[string]int) string {
	t.Helper()
	var resp Response
	require.NoError(t, json.Unmarshal(frame, &resp))
	key := string(resp.ID)
	seen[key]++
	require.LessOrEqual(t, seen[key], 1, "completion/cancellation must not duplicate responses")
	return key
}

func TestInboxWaitServe_OrdinaryToolsRemainInlineAndOrdered(t *testing.T) {
	h := newServeInboxHarness(t)
	entered := make(chan int32, 2)
	release := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	h.server.Registry().Register(ToolDef{Name: "mtix_ordinary", InputSchema: SchemaObj{Type: "object"}}, func(ctx context.Context, _ json.RawMessage) (*ToolsCallResult, error) {
		n := calls.Add(1)
		entered <- n
		if n == 1 {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return SuccessResult("ordinary"), nil
	})
	h.in.send(t, 1, MethodToolsCall, map[string]any{"name": "mtix_ordinary"})
	select {
	case n := <-entered:
		require.EqualValues(t, 1, n)
	case <-time.After(time.Second):
		t.Fatal("ordinary handler did not start")
	}
	h.in.send(t, 2, MethodToolsCall, map[string]any{"name": "mtix_ordinary"})
	select {
	case <-entered:
		t.Fatal("ordinary handlers must retain serial execution")
	case <-time.After(50 * time.Millisecond):
	}
	once.Do(func() { close(release) })
	require.Nil(t, h.response(t, 1).Error)
	require.Nil(t, h.response(t, 2).Error)
	require.EqualValues(t, 2, calls.Load())
	require.NoError(t, h.in.Close())
	h.finish(t, nil)
}
