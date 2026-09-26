// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Mutation-time warning for sync events over the wire cap (MTIX-95.12).
//
// The local field limits allow more than the hub accepts: a prompt may be
// 100 KB, but an event payload may be at most validator.MaxPayloadBytes
// (64 KB, FR-18.7). Such a mutation still succeeds locally, and `mtix sync
// push` then holds its event instead of sending it. So that the user learns
// this when making the change, emitEvent reports every event over the cap
// to the PayloadWarnings collector its context carries. The CLI and the MCP
// server install a collector only when a hub is configured, and print what
// it collected: one line per event, naming the field and the limit.

// PayloadWarning describes one sync event whose payload is over the wire
// cap: the node, the op, the field that makes up most of the payload
// (validator.LargestPayloadField), the payload size and the limit.
type PayloadWarning struct {
	NodeID       string
	OpType       model.OpType
	Field        string
	PayloadBytes int
	Limit        int
}

// String renders w as one warning line for the CLI and MCP. The advice fits
// the op (MTIX-95.12 round 3): a creation cannot be fixed by an edit, since
// push holds every later change of the task with it; any other op is fixed
// by shortening or splitting the field.
func (w PayloadWarning) String() string {
	field := oneLineText(w.Field)
	advice := fmt.Sprintf("sync push will hold it and not send it (shorten or split %s; see mtix sync doctor)", field)
	if w.OpType == model.OpCreateNode {
		advice = "this task's creation will be held with every later change of it; " +
			"do not edit it to fix this; see mtix sync doctor"
	}
	return fmt.Sprintf(
		"WARN: %s: the %s field makes this %s sync event %d bytes, over the %d-byte sync limit; saved locally, but %s",
		oneLineText(w.NodeID), field, oneLineText(string(w.OpType)), w.PayloadBytes, w.Limit, advice)
}

// PayloadWarnings collects the payload warnings of one command or tool
// call. It is safe for concurrent use.
type PayloadWarnings struct {
	mu   sync.Mutex
	list []PayloadWarning
}

// add records w unless an identical warning is already recorded.
func (c *PayloadWarnings) add(w PayloadWarning) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, seen := range c.list {
		if seen == w {
			return
		}
	}
	c.list = append(c.list, w)
}

// Drain returns the collected warnings in the order they were recorded and
// empties the collector.
func (c *PayloadWarnings) Drain() []PayloadWarning {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.list
	c.list = nil
	return out
}

// payloadWarningsKey is the context key of the PayloadWarnings collector.
type payloadWarningsKey struct{}

// WithPayloadWarnings returns a copy of ctx whose mutations report their
// sync events over the wire cap to c (MTIX-95.12).
func WithPayloadWarnings(ctx context.Context, c *PayloadWarnings) context.Context {
	return context.WithValue(ctx, payloadWarningsKey{}, c)
}

// afterEmit finishes an event emitEvent has written, in its transaction:
// it records the event's hook origin (FR-19.6, maybeRecordHookOrigin) and
// reports a payload over the wire cap to the context's collector
// (warnOversizedPayload, MTIX-95.12).
func afterEmit(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	if err := maybeRecordHookOrigin(ctx, tx, e.EventID); err != nil {
		return err
	}
	warnOversizedPayload(ctx, e)
	return nil
}

// warnOversizedPayload reports e to the PayloadWarnings collector of ctx
// when its payload is over validator.MaxPayloadBytes. emitEvent calls it for
// every event it writes; without a collector (no hub configured) it does
// nothing. The mutation is never refused: push holds the event later.
func warnOversizedPayload(ctx context.Context, e *model.SyncEvent) {
	if len(e.Payload) <= validator.MaxPayloadBytes {
		return
	}
	c, _ := ctx.Value(payloadWarningsKey{}).(*PayloadWarnings)
	if c == nil {
		return
	}
	c.add(PayloadWarning{
		NodeID: e.NodeID, OpType: e.OpType, Field: validator.LargestPayloadField(e.Payload),
		PayloadBytes: len(e.Payload), Limit: validator.MaxPayloadBytes,
	})
}

// oneLineText removes control characters from s and folds its whitespace
// into single spaces, so a warning stays on one line.
func oneLineText(s string) string {
	return strings.Join(strings.Fields(format.StripControlChars(s)), " ")
}
