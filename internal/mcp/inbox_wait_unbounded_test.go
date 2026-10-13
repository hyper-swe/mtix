// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The inbox wait tool distinguishes omitted, explicit-zero and invalid timeouts (FR-19).
package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

type inboxWaitRecordingBackend struct {
	calls   int
	timeout time.Duration
	ctx     context.Context
	err     error
}

func (b *inboxWaitRecordingBackend) InboxList(context.Context, string) ([]sqlite.InboxEvent, error) {
	return []sqlite.InboxEvent{{Seq: 7, Body: "Pending event"}}, nil
}
func (b *inboxWaitRecordingBackend) InboxAck(context.Context, string, int64) error { return nil }
func (b *inboxWaitRecordingBackend) InboxWait(ctx context.Context, _ string, timeout time.Duration) ([]sqlite.InboxEvent, error) {
	b.calls++
	b.timeout, b.ctx = timeout, ctx
	return []sqlite.InboxEvent{{Seq: 7, Body: "Pending event"}}, b.err
}

func TestInboxWaitTool_TimeoutContract_PreservesExplicitZeroAndDefault(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		want       time.Duration
	}{
		{"omitted default", `{"agent":"worker"}`, 60 * time.Second},
		{"explicit zero unbounded", `{"agent":"worker","timeout_seconds":0}`, 0},
		{"one second", `{"agent":"worker","timeout_seconds":1}`, time.Second},
		{"exact ceiling", `{"agent":"worker","timeout_seconds":60}`, 60 * time.Second},
		{"over ceiling", `{"agent":"worker","timeout_seconds":61}`, 60 * time.Second},
		{"large positive capped before duration conversion", `{"agent":"worker","timeout_seconds":9223372036854775807}`, 60 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testutil.OperatorState(t)
			backend := &inboxWaitRecordingBackend{}
			reg := NewToolRegistry()
			RegisterInboxTools(reg, testInboxService(backend))
			res, err := reg.Call(t.Context(), "mtix_inbox_wait", json.RawMessage(tc.args))
			require.NoError(t, err)
			assert.Equal(t, tc.want, backend.timeout)
			assert.Same(t, t.Context(), backend.ctx, "tool must preserve the caller's cancellation")
			assert.Equal(t, 1, backend.calls)
			require.Len(t, res.Content, 1)
			assert.False(t, res.IsError)
			assert.Equal(t, 1, strings.Count(res.Content[0].Text, "\nBEGIN MTIX MESSAGE\n"))
			assert.Contains(t, res.Content[0].Text, "\nseq: 7\n")
			assert.Contains(t, res.Content[0].Text, `body: "Pending event"`)
		})
	}
}

func TestInboxWaitTool_NegativeTimeout_RefusedBeforePendingEvent(t *testing.T) {
	backend := &inboxWaitRecordingBackend{}
	reg := NewToolRegistry()
	RegisterInboxTools(reg, testInboxService(backend))
	res, err := reg.Call(t.Context(), "mtix_inbox_wait", json.RawMessage(`{"agent":"worker","timeout_seconds":-1}`))
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Nil(t, res)
	assert.Zero(t, backend.calls, "invalid timeout must not reach a pending inbox")
}

func TestInboxWaitTool_TimeoutZero_CancellationPreservesError(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			backend := &inboxWaitRecordingBackend{err: failure}
			reg := NewToolRegistry()
			RegisterInboxTools(reg, testInboxService(backend))
			res, err := reg.Call(t.Context(), "mtix_inbox_wait", json.RawMessage(`{"agent":"worker","timeout_seconds":0}`))
			require.ErrorIs(t, err, failure)
			assert.Nil(t, res)
			assert.Zero(t, backend.timeout)
		})
	}
}

func TestInboxWaitTool_Help_DocumentsUnboundedAndDefault(t *testing.T) {
	reg := NewToolRegistry()
	RegisterInboxTools(reg, testInboxService(&inboxWaitRecordingBackend{}))
	for _, def := range reg.List() {
		if def.Name != "mtix_inbox_wait" {
			continue
		}
		assert.Contains(t, def.Description, "timeout_seconds=0")
		assert.Contains(t, def.Description, "cancellation")
		assert.Contains(t, def.InputSchema.Properties["timeout_seconds"].Description, "default 60")
		assert.Contains(t, def.InputSchema.Properties["timeout_seconds"].Description, "negative")
		return
	}
	t.Fatal("missing inbox wait tool")
}
