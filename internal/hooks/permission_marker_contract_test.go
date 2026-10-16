// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionMarked_NewConfirmationPrefixes_DoNotExpandLegacyRefusal(t *testing.T) {
	for _, key := range []string{"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "CLAUDECODE", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_ENV_FILE", "PI_SESSION_ID", "PI_SESSION_FILE", "PI_CODING_AGENT"} {
		t.Setenv(key, "")
	}
	for _, key := range []string{"CLAUDE_CODE_NEW_FIXTURE", "CODEX_SANDBOX_NEW_FIXTURE", "PI_NEW_FIXTURE"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "synthetic-marker-value")
			require.False(t, SessionMarked(), "hook refusal remains its exact legacy predicate")
			assert.Equal(t, []string{key}, oplocal.HarnessSessionMarkers([]string{key + "=synthetic-marker-value"}))
		})
	}
}
