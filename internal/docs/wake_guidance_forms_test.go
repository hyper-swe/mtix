// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Routine form tests cover active, commented, replaced and added examples.
package docs

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func wakeGuidanceForms() []string {
	return []string{
		`claude -p "$PAYLOAD"`, `/usr/bin/printf '%s\n' "$PAYLOAD" | claude -p`,
		`env printf '%s\n' "$PAYLOAD" | claude -p`, `echo "$PAYLOAD" | claude -p`,
		`cat <<< "$PAYLOAD" | claude -p`, `claude -p <<< "$PAYLOAD"`,
		`printf '%s\n' "$PAYLOAD" | xargs claude -p`, `eval "claude -p $PAYLOAD"`,
		`gemini -p "$PAYLOAD"`, `claude -p "${PAYLOAD}"`, `claude "$P"`,
		`codex queue --message "$PAYLOAD"`, `new-runtime "$(mtix inbox --agent w --format prompt)"`,
		`printf "$PAYLOAD\n" | claude -p`, `mtix inbox --agent w --format prompt | claude -p`,
		`mtix inbox --agent w --format prompt | xargs -0 gemini -p`,
	}
}

func assertWakeGuidanceForms(t *testing.T) {
	t.Helper()
	paths := []string{"examples/hooks/wake-agent.sh", "USERMANUAL.md", "internal/docs/templates/skills/multi_agent.md.tmpl", ".claude-plugin/skills/mtix-multi-agent.md", ".codex-plugin/skills/multi-agent/SKILL.md"}
	for _, path := range paths {
		original := wakeSource(t, path)
		document := wakeDocumentFile(path, original)
		require.Empty(t, wakeLaunchProblems(original, document), path)
		for index, form := range wakeGuidanceForms() {
			for _, second := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/%t", path, index, second), func(t *testing.T) {
					changed := wakeReplaceGuidance(t, original, form, document, second)
					require.NotEmpty(t, wakeLaunchProblems(changed, document))
				})
			}
		}
	}
}

func wakeReplaceGuidance(t *testing.T, text, form string, document, second bool) string {
	t.Helper()
	if document && second {
		return text + "\n- Example: `" + form + "`\n"
	}
	needle := `printf '%s\n' "$PAYLOAD" | claude -p`
	if !document {
		needle = `printf '%s\n' "$PAYLOAD" | "$HARNESS_BIN" -p &`
		if second {
			needle = `# printf '%s\n' "$PAYLOAD" | "$HARNESS_BIN" exec - &`
			form = "# " + form
		}
	}
	require.Contains(t, text, needle)
	return strings.Replace(text, needle, form, 1)
}
