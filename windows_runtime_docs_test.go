// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The Windows maintainer runbook must describe the two reviewed heads and
// actual runtime/fixture merge gates, including failure and spend accounting.
package mtix_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCI_WindowsRuntimeDocumentation_MergeGatesExplained(t *testing.T) {
	src, err := os.ReadFile("docs/WINDOWS-RUNTIME.md")
	require.NoError(t, err)
	for _, text := range []string{"windows-ci", "non-draft", "re-add", "label-only", "15 minutes", "8 minutes", "UNPROVEN", "second reviewed head", "captured user SID", "started_at", "completed_at", "failed", "canceled", "short path names are unavailable on this volume", "Do not weaken", "MTIX-130.9", "MTIX-130.15"} {
		require.Contains(t, string(src), text)
	}
}
