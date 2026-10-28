// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

// Reflected-text escaping must be documented for agents (MTIX-187.7.8).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReflectedTextDocs_EscapingIsDocumentedWithMirrors(t *testing.T) {
	for _, path := range []string{
		"internal/docs/templates/skills/sync.md.tmpl", ".claude-plugin/skills/mtix-sync.md",
		".codex-plugin/skills/sync/SKILL.md", "USERMANUAL.md",
	} {
		t.Run(path, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", path))
			require.NoError(t, err)
			require.Contains(t, string(body), "An id or text a caller sends that a server repeats in an error or result message is escaped in the message")
			require.Contains(t, string(body), "`"+"\\n`, `"+"\\r`, `"+"\\t`, `"+"\\xNN`, `"+"\\uNNNN` or `"+"\\UNNNNNNNN`")
			require.Contains(t, string(body), "the JSON fields still carry what was sent")
			require.Contains(t, string(body), "can still hold the raw id. An id or text a caller sends", "the sentence follows the validator and hub sentence")
		})
	}
}
