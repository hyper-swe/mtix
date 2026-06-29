// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// These guard-clause tests need no PG: they exercise the nil-pool and
// empty-argument validation that must fail BEFORE any connection is used.

func TestSweepDuplicates_NilPool(t *testing.T) {
	var p *transport.Pool
	_, err := p.SweepDuplicates(context.Background(), "MTIX")
	require.Error(t, err)
}

func TestSweepDuplicates_EmptyPrefix(t *testing.T) {
	// A closed pool is also "not open"; a fresh zero-value Pool likewise.
	var p transport.Pool
	_, err := p.SweepDuplicates(context.Background(), "")
	require.Error(t, err)
}

func TestPreviewDuplicates_NilPool(t *testing.T) {
	var p *transport.Pool
	_, err := p.PreviewDuplicates(context.Background(), "MTIX")
	require.Error(t, err)
}

func TestPreviewDuplicates_EmptyPrefix(t *testing.T) {
	var p transport.Pool
	_, err := p.PreviewDuplicates(context.Background(), "")
	require.Error(t, err)
}

func TestEnsureRegistryIndex_NilPool(t *testing.T) {
	var p *transport.Pool
	_, err := p.EnsureRegistryIndex(context.Background(), "MTIX")
	require.Error(t, err)
}

func TestEnsureRegistryIndex_EmptyPrefix(t *testing.T) {
	var p transport.Pool
	_, err := p.EnsureRegistryIndex(context.Background(), "")
	require.Error(t, err)
}
