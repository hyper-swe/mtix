// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-294 (r2b): an unclaimed intermediate whose descendants are all done
// reads 1.0 and credits its parent, and verify uses the same definition.
func TestRollup_OpenIntermediateAllDone_CreditsAncestorAndVerifies(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		s := rollupStore(t, weighted)
		rollupChain(t, s, "PROJ-1", "PROJ-1.1", "PROJ-1.1.1")
		claimAndDone(t, s, "PROJ-1.1.1")

		assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1.1"), "weighted=%v", weighted)
		assert.Equal(t, 1.0, rollupProgress(t, s, "PROJ-1"), "weighted=%v", weighted)
		res, err := s.Verify(context.Background())
		require.NoError(t, err)
		assert.True(t, res.ProgressOK, "weighted=%v errors=%v", weighted, res.Errors)
	}
}

// A claimed intermediate does not credit its parent, and verify agrees.
func TestRollup_ClaimedIntermediate_VerifyMatchesRollup(t *testing.T) {
	for _, weighted := range []bool{false, true} {
		s := rollupStore(t, weighted)
		rollupChain(t, s, "PROJ-1", "PROJ-1.1", "PROJ-1.1.1")
		require.NoError(t, s.ClaimNode(context.Background(), "PROJ-1.1", "agent-1"))
		claimAndDone(t, s, "PROJ-1.1.1")

		assert.Less(t, rollupProgress(t, s, "PROJ-1"), 1.0, "weighted=%v", weighted)
		res, err := s.Verify(context.Background())
		require.NoError(t, err)
		assert.True(t, res.ProgressOK, "weighted=%v errors=%v", weighted, res.Errors)
	}
}
