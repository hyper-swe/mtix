// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package benchmarks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// poolMaxConnsCeiling is the FR-18 / MTIX-15.10 per-CLI ceiling.
// At 10 active developers per hub × 5 conns/CLI = 50 connections,
// well within the 100-conn limits of common managed PG providers.
const poolMaxConnsCeiling int32 = 5

// TestPerf_PoolMaxConnections asserts the production
// transport.DefaultPoolDefaults().MaxConns honors the FR-18 / MTIX-15.10
// ceiling. Static check — no connection opened — so this runs in
// every default test pass with no PG dependency.
func TestPerf_PoolMaxConnections(t *testing.T) {
	defs := transport.DefaultPoolDefaults()
	require.LessOrEqualf(t, defs.MaxConns, poolMaxConnsCeiling,
		"DefaultPoolDefaults.MaxConns=%d exceeds FR-18 ceiling of %d",
		defs.MaxConns, poolMaxConnsCeiling)
}
