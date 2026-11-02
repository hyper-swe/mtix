// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package mcp

import "context"

// AddressedSendHook runs after an addressed annotation has committed
// (ADR-007 D21, MTIX-112.20.2). It requests a push of the board's pending
// events and returns a short state word for the tool result, or "" to say
// nothing. It must return before any network work finishes.
type AddressedSendHook func(ctx context.Context, to string) string

type contextConfig struct{ onAddressedSend AddressedSendHook }

// ContextOption configures RegisterContextTools.
type ContextOption func(*contextConfig)

// WithAddressedSendHook wires the post-commit push request of an addressed
// mtix_annotate (MTIX-112.20.2).
func WithAddressedSendHook(h AddressedSendHook) ContextOption {
	return func(c *contextConfig) { c.onAddressedSend = h }
}
