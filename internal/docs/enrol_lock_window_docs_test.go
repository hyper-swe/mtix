// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enrolLockWindowSentence is the one operator sentence, identical in every
// copy, that states the accepted move-then-recheck window of the enrolment
// lock fix and its real consequence (MTIX-112.17.16.21.2.2, DESK-1.248).
const enrolLockWindowSentence = "The move is not atomic with its check, and this is accepted: a lock a live enrolment creates in a microsecond window is moved under the aside name (enroll.lock.unprobeable) and the fix puts it back by checking the lock path and then renaming, which is itself not atomic; if a third process took a fresh lock at the lock path first, the put-back is refused and the fix fails with an error naming the aside file, and a lock created between that check and the rename is replaced, so two holders can exist; after a refused put-back the held lock stays under the aside name for the rest of that enrolment, so later probes see the lock as free, other commands can write until the enrolment writes its marker, and a second enrolment can run at the same time, and doctor does not report the aside file afterwards; run the fix with no other mtix process in the worktree, and run one enrolment at a time."

// enrolLockWindowCopies are the nine operator-facing copies, relative to the
// repository root.
var enrolLockWindowCopies = []string{
	"internal/docs/templates/troubleshooting.md.tmpl",
	"docs/TROUBLESHOOTING.md",
	"USERMANUAL.md",
	"internal/docs/templates/skills/admin.md.tmpl",
	"internal/docs/templates/skills/multi_agent.md.tmpl",
	".claude-plugin/skills/mtix-admin.md",
	".claude-plugin/skills/mtix-multi-agent.md",
	".codex-plugin/skills/admin/SKILL.md",
	".codex-plugin/skills/multi-agent/SKILL.md",
}

// enrolLockWindowDrifted returns the copies under root that do not carry the
// sentence, comparing with whitespace flattened so line wrapping is free.
func enrolLockWindowDrifted(t *testing.T, root string) []string {
	t.Helper()
	var drifted []string
	for _, rel := range enrolLockWindowCopies {
		body, err := os.ReadFile(filepath.Join(root, rel))
		require.NoError(t, err, rel)
		if !strings.Contains(strings.Join(strings.Fields(string(body)), " "), enrolLockWindowSentence) {
			drifted = append(drifted, rel)
		}
	}
	return drifted
}

// TestEnrolLockWindowDocs_SentenceIdenticalInEveryCopy: all nine copies carry
// the one sentence on the enrolment lock fix window.
func TestEnrolLockWindowDocs_SentenceIdenticalInEveryCopy(t *testing.T) {
	assert.Empty(t, enrolLockWindowDrifted(t, filepath.Join("..", "..")))
}

// TestEnrolLockWindowDocs_OneDriftedCopy_IsReported proves the guard: copy the
// nine files, let one drift (the old overclaim returns), and expect exactly
// that copy to be named.
func TestEnrolLockWindowDocs_OneDriftedCopy_IsReported(t *testing.T) {
	for _, drift := range enrolLockWindowCopies {
		t.Run(drift, func(t *testing.T) {
			tmp := t.TempDir()
			for _, rel := range enrolLockWindowCopies {
				body, err := os.ReadFile(filepath.Join("..", "..", rel))
				require.NoError(t, err)
				if rel == drift {
					body = []byte(strings.Replace(string(body), "which is itself not atomic", "so two holders are briefly possible", 1))
				}
				dst := filepath.Join(tmp, rel)
				require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o700))
				require.NoError(t, os.WriteFile(dst, body, 0o600))
			}
			assert.Equal(t, []string{drift}, enrolLockWindowDrifted(t, tmp))
		})
	}
}
