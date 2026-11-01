// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestDispatcherLock_OnePerOperator_ReleasedOnClose(t *testing.T) {
	state := testutil.OperatorState(t)
	first, err := state.AcquireDispatcher(t.Context())
	require.NoError(t, err)
	second, err := state.AcquireDispatcher(t.Context())
	require.Error(t, err)
	require.Nil(t, second)
	require.NoError(t, first.Close())
	next, err := state.AcquireDispatcher(t.Context())
	require.NoError(t, err)
	require.NoError(t, next.Close())
}
