// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import (
	"context"

	"github.com/hyper-swe/mtix/internal/service"
)

// AddressedSendHook runs after an addressed annotation has committed
// (ADR-007 D21, MTIX-112.20.2). It requests a push of the board's pending
// events and returns a short state word for the tool result, or "" to say
// nothing. It must return before any network work finishes.
type AddressedSendHook func(ctx context.Context, to string) string

// ReplyWindowHook records the sender's reply window after an annotation sent
// with await_reply committed (MTIX-112.20.3). It returns a warning for the
// caller to show, or empty.
type ReplyWindowHook func(ctx context.Context, node, author string, res service.AnnotationResult) string

type contextConfig struct {
	onAddressedSend AddressedSendHook
	onReplyWindow   ReplyWindowHook
}

// WithReplyWindowHook wires the sender-window recorder of mtix_annotate.
func WithReplyWindowHook(h ReplyWindowHook) ContextOption {
	return func(c *contextConfig) { c.onReplyWindow = h }
}

// ContextOption configures RegisterContextTools.
type ContextOption func(*contextConfig)

// WithAddressedSendHook wires the post-commit push request of an addressed
// mtix_annotate (MTIX-112.20.2).
func WithAddressedSendHook(h AddressedSendHook) ContextOption {
	return func(c *contextConfig) { c.onAddressedSend = h }
}
