// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestRepairCreateUIDs_NilPool(t *testing.T) {
	var p *transport.Pool
	_, err := p.RepairCreateUIDs(context.Background(), "MTIX", nil, false)
	require.ErrorContains(t, err, "pool not open")
}

func TestRepairCreateUIDs_EmptyPrefix(t *testing.T) {
	p := &transport.Pool{}
	_, err := p.RepairCreateUIDs(context.Background(), "", nil, false)
	require.ErrorContains(t, err, "pool not open") // the nil inner pool is checked first
}
