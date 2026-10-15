// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"context"
	"io"
)

// VerbRequest is one attach verb call from harness glue, after the handshake.
// Protocol is the protocol being served, which may be N-1.
type VerbRequest struct {
	Verb     string
	Harness  string
	Protocol int
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
}

// Adapter performs the attach verbs for one harness. Adapters ship with their
// harness integrations; a harness without an adapter is served inbox-only.
type Adapter interface {
	Run(ctx context.Context, req VerbRequest) error
}
