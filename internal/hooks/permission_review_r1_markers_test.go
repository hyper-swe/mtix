// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks_test

import (
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewR1_MarkerAgreementIncludesClaudeEnvironment(t *testing.T) {
	t.Setenv("CLAUDE_ENV_FILE", "synthetic-marker-value")
	require.True(t, hooks.SessionMarked())
	assert.Contains(t, oplocal.HarnessSessionMarkers(os.Environ()), "CLAUDE_ENV_FILE")
}
