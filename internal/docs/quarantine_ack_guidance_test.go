// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Recovery guidance stays accurate on always-loaded and workflow surfaces (MTIX-187.2).
package docs

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestQuarantineAcknowledgementGuidance_ManagedAndWorkflowRecovery(t *testing.T) {
	for _, path := range []string{"templates/agents.md.tmpl", "templates/claude.md.tmpl", "templates/workflows/small-team.md.tmpl", "templates/workflows/safety-critical.md.tmpl"} {
		t.Run(path, func(t *testing.T) {
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			text := string(content)
			require.Contains(t, text, "mtix sync quarantine acknowledge <event-id> --reason <text>")
			require.Contains(t, text, "no unacknowledged pulled event remains")
			require.Contains(t, text, "retained and unapplied")
			require.NotContains(t, text, "quarantined events` check while any remain")
		})
	}
}
