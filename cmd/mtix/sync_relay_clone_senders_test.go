// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Recovery deliberately uses operator quiescence, not a runtime send guard.
// Pin each independent publisher at each stop, including all shipped copies.
import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestRelayCloneRecovery_AddressedSenders_ExplicitStopPerSurface(t *testing.T) {
	for _, tc := range []struct{ name, instruction string }{
		{"CLI", "do not run `mtix comment --to` or `mtix annotate --to`"},
		{"MCP", "stop the MCP server (`mtix mcp`) in its client and disable automatic restart"},
		{"serve", "stop `mtix serve` where it runs (Ctrl-C in its terminal, or stop its supervisor and disable automatic restart)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, index := range []int{6, 7} {
				assert.Contains(t, sqlite.RelayCloneOnSyncedSteps()[index].Text, tc.instruction, "step %d", index+1)
			}
			assert.Contains(t, sqlite.RelayCloneOnSyncedWays(), tc.instruction)
			for _, copy := range recoveryDocCopies {
				t.Run(copy.path, func(t *testing.T) {
					raw, err := os.ReadFile(filepath.Join("..", "..", copy.path))
					require.NoError(t, err)
					assert.Equal(t, copy.stops, strings.Count(string(raw), tc.instruction))
				})
			}
		})
	}
}

func TestRelayCloneRecovery_AddressedSenders_ExplainsLifetimeAndRisk(t *testing.T) {
	for _, words := range []string{
		"Addressed sends publish even with every daemon stopped",
		"retiring a peer does not disable these senders",
		"Pause agents, hooks and automation that can send or restart these processes",
		"Wait for in-flight sends to finish before continuing: a send starts a push that can run for up to five minutes, so wait at least five minutes after the last send",
		"Keep these senders stopped in the old project through retirement and never resume them there after retirement",
		"Resume them only in the new project after the recovery checks pass, or in the old project if recovery is abandoned before retirement",
	} {
		for _, index := range []int{6, 7} {
			assert.Contains(t, sqlite.RelayCloneOnSyncedSteps()[index].Text, words)
		}
		for _, copy := range recoveryDocCopies {
			raw, err := os.ReadFile(filepath.Join("..", "..", copy.path))
			require.NoError(t, err)
			assert.Equal(t, copy.stops, strings.Count(string(raw), words), copy.path)
		}
	}
}

// oldProjectSwitchWords is the switch instruction, which belongs to the OLD
// project only (step 8): the new project keeps the default.
const oldProjectSwitchWords = "in the old project only, record its current value with `mtix config get sync.push_on_addressed_send`, then run `mtix config set sync.push_on_addressed_send false`, which every addressed send rereads, so it covers the CLI, MCP and serve (a send made with it off still commits locally and is not published, so keep to the stops above); if recovery is abandoned before retirement, restore the recorded value there with `mtix config set sync.push_on_addressed_send <recorded value>`, never an assumed true"

// recoveryRestoreWords is the step 9 STOP clause that puts the recorded value back.
const recoveryRestoreWords = "restore the old project's recorded `sync.push_on_addressed_send` value with `mtix config set sync.push_on_addressed_send <recorded value>`"

func TestRelayCloneRecovery_OldProjectSwitch_OnlyInOldProjectStep(t *testing.T) {
	steps := sqlite.RelayCloneOnSyncedSteps()
	assert.NotContains(t, steps[6].Text, "sync.push_on_addressed_send", "step 7 stops the NEW project and must not set the switch")
	assert.Contains(t, steps[7].Text, oldProjectSwitchWords, "step 8 stops the old project")
	for _, copy := range recoveryDocCopies {
		raw, err := os.ReadFile(filepath.Join("..", "..", copy.path))
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(string(raw), oldProjectSwitchWords), copy.path)
	}
}

func TestRelayCloneRecovery_StopProse_RestoresRecordedSwitchValue(t *testing.T) {
	assert.Contains(t, sqlite.RelayCloneOnSyncedStopHalfJoined, recoveryRestoreWords)
	for _, copy := range []struct {
		path string
		n    int
	}{
		{"internal/docs/templates/skills/sync.md.tmpl", 2},
		{".claude-plugin/skills/mtix-sync.md", 2},
		{".codex-plugin/skills/sync/SKILL.md", 2},
		{"USERMANUAL.md", 1},
		{"internal/docs/templates/troubleshooting.md.tmpl", 1},
		{"docs/TROUBLESHOOTING.md", 1},
	} {
		raw, err := os.ReadFile(filepath.Join("..", "..", copy.path))
		require.NoError(t, err)
		assert.Equal(t, copy.n, strings.Count(string(raw), recoveryRestoreWords), copy.path)
	}
}
