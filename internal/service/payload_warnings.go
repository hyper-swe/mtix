// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// PayloadWarnings owns per-operation sync warnings per MTIX-95.12.
// The durable collector preserves concurrency safety, deduplication and order.
type PayloadWarnings struct{ backend sqlite.PayloadWarnings }

// WithPayloadWarnings attaches the service warning collector per MTIX-95.12.
func WithPayloadWarnings(ctx context.Context, warnings *PayloadWarnings) context.Context {
	return sqlite.WithPayloadWarnings(ctx, &warnings.backend)
}

// Drain returns exact warning text and empties the per-operation collector.
func (c *PayloadWarnings) Drain() []string {
	warnings := c.backend.Drain()
	var out []string
	for _, warning := range warnings {
		out = append(out, warning.String())
	}
	return out
}
