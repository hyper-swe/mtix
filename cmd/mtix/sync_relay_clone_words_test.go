// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// These tests pin recovery instructions at their point of use, including all
// shipped doc copies, so unrelated command mentions cannot hide a missing step.
import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// recoveryDocCopies maps each shipped copy of the rejoin recovery to how many
// times it names the daemon stop (uninstall) and the daemon restart (install).
var recoveryDocCopies = []struct {
	path            string
	stops, restarts int
}{
	{"internal/docs/templates/skills/sync.md.tmpl", 2, 2},
	{".claude-plugin/skills/mtix-sync.md", 2, 2},
	{".codex-plugin/skills/sync/SKILL.md", 2, 2},
	{"USERMANUAL.md", 2, 1},
	{"internal/docs/templates/troubleshooting.md.tmpl", 1, 1},
	{"docs/TROUBLESHOOTING.md", 1, 1},
}

var daemonVerbMention = regexp.MustCompile("`mtix daemon ([a-z]+)`")

// TestRelayCloneOnSyncedSteps_Words_NameRecoveryContext pins the words of
// every step of the recovery against the steps the refusal prints.
func TestRelayCloneOnSyncedSteps_Words_NameRecoveryContext(t *testing.T) {
	requireStepWords(t, sqlite.RelayCloneOnSyncedSteps())
}

// TestRelayCloneOnSyncedSteps_Docs_NameRecoveryContext pins, in each doc copy,
// the old-peer-id source, the new-project tick, and the daemon stop and
// restart notes at EACH mention, not once per copy.
func TestRelayCloneOnSyncedSteps_Docs_NameRecoveryContext(t *testing.T) {
	for _, c := range recoveryDocCopies {
		t.Run(c.path, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", c.path))
			require.NoError(t, err)
			var recovery string
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.Contains(line, "retire-peer <old-peer-id>") {
					recovery += line
				}
			}
			require.NotEmpty(t, recovery)
			for _, words := range []string{
				"`mtix config get sync.relay.peer_id` in the old project",
				"unset means the machine id",
				"`this peer` line of `mtix sync relay status` in the old project",
				"run `mtix sync relay tick` in the new project",
			} {
				assert.Contains(t, recovery, words)
			}
			stopMention := "`mtix daemon stop`, then `mtix daemon uninstall` " + sqlite.RelayCloneOnSyncedDaemonStopNote
			restartMention := "`mtix daemon install` " + sqlite.RelayCloneOnSyncedDaemonRestartNote
			assert.Equal(t, c.stops, strings.Count(recovery, stopMention), "uninstall mention followed by its note")
			assert.Equal(t, c.restarts, strings.Count(recovery, restartMention), "install mention followed by its note")
			// Every other daemon verb in the line must be one the notes name.
			for _, m := range daemonVerbMention.FindAllStringSubmatch(recovery, -1) {
				assert.Contains(t, []string{"uninstall", "install", "status", "stop"}, m[1], "unexpected daemon verb %q", m[1])
			}
		})
	}
}

// TestRelayCloneOnSyncedSteps_DaemonNote_NamesPlatformFacts pins the stop
// note facts: stop then uninstall then status, the harmless not-installed
// error, the macOS caveat, the Windows instance end, and an honest listener
// wait.
func TestRelayCloneOnSyncedSteps_DaemonNote_NamesPlatformFacts(t *testing.T) {
	note := sqlite.RelayCloneOnSyncedDaemonStopNote
	for _, w := range []string{
		"run `mtix daemon stop` first", "ends a running instance on Windows", "on macOS launchd restarts it, so stop alone does not hold",
		"then `mtix daemon uninstall`", "then check that `mtix daemon status` fails",
		"not-installed error", "expected and harmless: continue", "within one pass",
	} {
		assert.Contains(t, note, w)
	}
	assert.NotContains(t, note, "about 10 seconds")
	assert.NotContains(t, note, "succeeds when none is installed")
}

// TestRelayCloneOnSyncedSteps_DaemonNote_PinnedInStepsAndStopText pins the
// stop note in steps 7 and 8, the restart note in the step 9 STOP text, and
// that no step still relies on mtix daemon stop.
func TestRelayCloneOnSyncedSteps_DaemonNote_PinnedInStepsAndStopText(t *testing.T) {
	steps := sqlite.RelayCloneOnSyncedSteps()
	for _, i := range []int{6, 7} {
		assert.Contains(t, steps[i].Text, "with `mtix daemon stop`, then `mtix daemon uninstall` "+sqlite.RelayCloneOnSyncedDaemonStopNote, "step %d", i+1)
	}
	assert.Contains(t, steps[8].Text, "with `mtix daemon install` "+sqlite.RelayCloneOnSyncedDaemonRestartNote)
	assert.Contains(t, sqlite.RelayCloneOnSyncedStopHalfJoined, "`mtix daemon install` "+sqlite.RelayCloneOnSyncedDaemonRestartNote)
}
