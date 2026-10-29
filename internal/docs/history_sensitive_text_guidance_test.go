// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func sensitiveTextGuideSection(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", path)) //nolint:gosec // fixed repository documentation paths
	require.NoError(t, err)
	const heading = "Removing sensitive text (0.6.0)"
	start := strings.Index(string(data), heading)
	require.NotEqual(t, -1, start, "%s needs the cleanup guide", path)
	body := string(data)[start+len(heading):]
	if end := regexp.MustCompile(`(?m)^#{1,4} `).FindStringIndex(body); end != nil {
		body = body[:end[0]]
	}
	return strings.ToLower(strings.Join(strings.Fields(body), " "))
}

func TestHistorySensitiveTextGuidance(t *testing.T) {
	for _, path := range []string{
		"USERMANUAL.md", "internal/docs/templates/skills/sync.md.tmpl",
		".claude-plugin/skills/mtix-sync.md", ".codex-plugin/skills/sync/SKILL.md",
	} {
		t.Run(path, func(t *testing.T) {
			body := sensitiveTextGuideSection(t, path)
			for _, term := range []string{"clean value", "history.export", "false", "local sqlite", "queued", "git history", "force-push", "re-clone", "protocol-2", "never paste secrets"} {
				require.Contains(t, body, term)
			}
			require.Regexp(t, `no supported command[^.]*selectively[^.]*history`, body)
			require.Regexp(t, `cannot erase[^.]*hub`, body)
			require.Regexp(t, `retention[^.]*not[^.]*erasure`, body)
		})
	}
}

func TestHistorySensitiveTextExportBoundary(t *testing.T) {
	body := sensitiveTextGuideSection(t, "docs/EXPORT-FORMAT.md")
	for _, term := range []string{"history.export", "false", "current fields", "local sqlite", "hub", "git history", "other clones", "protocol-2", "../usermanual.md#removing-sensitive-text-060"} {
		require.Contains(t, body, term)
	}
}
