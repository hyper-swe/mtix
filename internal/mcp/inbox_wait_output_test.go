// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A stalled owned stdout pipe must be interruptible without taking the write
// mutex: one completed wait may own it while another waits to acquire it.
type serveInboxBlockedOutput struct {
	entered   chan struct{}
	closed    chan struct{}
	writeOnce sync.Once
	closeOnce sync.Once
	closeErr  error
}

func TestInboxWaitServe_ExcessiveExponentRejectedWithoutWaitAllocation(t *testing.T) {
	h := newServeInboxHarness(t)
	// A modest exponent exercises the numeric-key budget without probing
	// million-digit canonical-key amplification.
	id := json.RawMessage("1e1025")
	h.in.send(t, id, MethodToolsCall, map[string]any{"name": "mtix_inbox_wait", "arguments": map[string]any{"agent": "worker", "timeout_seconds": 0}})
	resp := h.response(t, id)
	require.NotNil(t, resp.Error)
	assert.Equal(t, ErrCodeInvalidRequest, resp.Error.Code)
	assert.Empty(t, h.backend.started, "oversized numeric ID must not acquire a wait worker")
	h.in.send(t, 3, MethodPing, nil)
	require.Nil(t, h.response(t, 3).Error)
	require.NoError(t, h.in.Close())
	h.finish(t, nil)
}

func TestInboxWaitServe_EquivalentNumericIDTargetsCancellation(t *testing.T) {
	h := newServeInboxHarness(t)
	call := h.wait(t, json.RawMessage("1e3"), map[string]any{"agent": "worker", "timeout_seconds": 0})
	h.in.send(t, nil, "notifications/cancelled", map[string]any{"requestId": 1000})
	requireServeInboxExited(t, call)
	require.ErrorIs(t, call.ctx.Err(), context.Canceled)
	h.in.send(t, 3, MethodPing, nil)
	require.Nil(t, h.response(t, 3).Error)
	require.NoError(t, h.in.Close())
	h.finish(t, nil)
}

func (out *serveInboxBlockedOutput) Write([]byte) (int, error) {
	out.writeOnce.Do(func() { close(out.entered) })
	<-out.closed
	return 0, io.ErrClosedPipe
}

func (out *serveInboxBlockedOutput) Close() error {
	out.closeOnce.Do(func() { close(out.closed) })
	return out.closeErr
}

func TestInboxWaitServe_ReadAndCloseErrorsBothPropagateAfterJoin(t *testing.T) {
	closeFailure := errors.New("owned output close failure")
	out := &serveInboxBlockedOutput{entered: make(chan struct{}), closed: make(chan struct{}), closeErr: closeFailure}
	h := newServeInboxHarnessWithWriter(t, out)
	call := h.wait(t, 2, map[string]any{"agent": "worker", "timeout_seconds": 0})
	readFailure := errors.New("owned input read failure")
	h.in.fail(readFailure)
	select {
	case err := <-h.done:
		h.joined = true
		require.ErrorIs(t, err, readFailure)
		require.ErrorIs(t, err, closeFailure)
	case <-time.After(time.Second):
		t.Fatal("read/close failure did not join the session")
	}
	requireServeInboxExited(t, call)
	require.ErrorIs(t, call.ctx.Err(), context.Canceled)
}

func TestInboxWaitServe_SchedulingMetadataStaysInternalAndNarrow(t *testing.T) {
	h := newServeInboxHarness(t)
	for _, def := range h.server.Registry().List() {
		assert.Equal(t, def.Name == "mtix_inbox_wait", def.CancellableWait)
	}
	h.in.send(t, 1, MethodToolsList, nil)
	resp := h.response(t, 1)
	var result struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(mustServeInboxJSON(t, resp.Result), &result))
	require.Len(t, result.Tools, 3)
	for _, def := range result.Tools {
		assert.NotContains(t, def, "CancellableWait")
		assert.NotContains(t, def, "cancellableWait")
	}
	require.NoError(t, h.in.Close())
	h.finish(t, nil)
}

func TestInboxWaitServe_ShutdownUnblocksOwnedWriterAndJoinsQueuedResponses(t *testing.T) {
	for _, reason := range []string{"EOF", "cancel"} {
		t.Run(reason, func(t *testing.T) {
			out := &serveInboxBlockedOutput{entered: make(chan struct{}), closed: make(chan struct{})}
			h := newServeInboxHarnessWithWriter(t, out)
			// On behavioral RED, release only our writer before fixture joins.
			t.Cleanup(func() { assert.NoError(t, out.Close()) })
			first := h.wait(t, 1, map[string]any{"agent": "first", "timeout_seconds": 0})
			close(first.event)
			select {
			case <-out.entered:
			case <-time.After(time.Second):
				t.Fatal("completed wait did not reach owned stdout")
			}
			second := h.wait(t, 2, map[string]any{"agent": "second", "timeout_seconds": 0})
			close(second.event)
			requireServeInboxExited(t, second)
			var want error
			if reason == "EOF" {
				require.NoError(t, h.in.Close())
			} else {
				h.cancel()
				want = context.Canceled
			}
			h.finish(t, want)
			requireServeInboxExited(t, first)
			select {
			case <-out.closed:
			default:
				t.Fatal("session shutdown did not close its owned blocked writer")
			}
		})
	}
}
