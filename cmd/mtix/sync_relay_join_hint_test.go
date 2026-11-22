// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests the relay join recovery messages and their command grammar (MTIX-263.1).
package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/relay/lifecycle"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
)

func TestRelayAttach_NoSharedProject_ExplainsCloneFirst(t *testing.T) {
	for _, history := range []string{"empty", "unrelated"} {
		t.Run(history, func(t *testing.T) {
			initTestApp(t)
			cacheCollisionMachineHash(t)
			setDiagnosticConfig(t, "sync.relay.require_auth", "false")
			if history == "unrelated" {
				require.NoError(t, runCreate("Unrelated local history", "", "", 3, "", "", "", "", ""))
			}
			dir := t.TempDir()
			_, err := lifecycle.Init(lifecycle.InitRequest{
				RelayDir: dir, RelayID: "test-relay", CreatedAt: time.Unix(1, 0).UTC(),
				CreatedBy: diagnosticOther,
				Projects:  []metadata.Project{{Prefix: "OTHER", FirstEventHash: "other-history"}},
			})
			require.NoError(t, err)
			out, diagnostic, err := relayCommandOutput(newRelayAttachCmd(), dir)
			require.ErrorIs(t, err, lifecycle.ErrNoSharedProject)
			require.Contains(t, diagnostic, err.Error())
			require.NotContains(t, out, "attached to relay")
			require.Contains(t, err.Error(), "clone-first")
			require.NotContains(t, err.Error(), "init your own relay")
			require.NotContains(t, err.Error(), "mtix sync relay init")
			require.Contains(t, err.Error(), "before or after clone")
		})
	}
}

func TestRelayAttachHint_NoSharedProject_CommandsParseAndMatchDocs(t *testing.T) {
	for _, err := range []error{lifecycle.ErrNoSharedProject, fmt.Errorf("attach: %w", lifecycle.ErrNoSharedProject)} {
		hint := relayAttachHint(err)
		require.ErrorIs(t, hint, lifecycle.ErrNoSharedProject)
		calls := mtixInvocations(hint.Error())
		require.NotEmpty(t, calls)
		root := newRootCmd()
		for _, args := range calls {
			require.Empty(t, invocationProblem(root, args), args)
			cmd, remaining, findErr := root.Find(args)
			require.NoError(t, findErr)
			require.NoError(t, cmd.ParseFlags(remaining))
			require.NoError(t, cmd.ValidateArgs(cmd.Flags().Args()))
		}
		joined := make([]string, 0, len(calls))
		for _, args := range calls {
			joined = append(joined, "mtix "+strings.Join(args, " "))
		}
		for _, command := range lateJoinerCommands(t, "internal/docs/templates/skills/sync.md.tmpl") {
			if !strings.HasPrefix(command, "#") {
				require.Contains(t, joined, command)
			}
		}
		require.Contains(t, joined, "mtix sync relay clone --export")
		require.Contains(t, joined, "mtix config set sync.relay.require_auth false")
		assertRelayJoinHintInstructions(t, hint.Error())
	}
}

// assertRelayJoinHintInstructions pins the non-command instructions of the
// clone-first join hint, so dropping any of them from the text fails here.
func assertRelayJoinHintInstructions(t *testing.T, text string) {
	t.Helper()
	for _, phrase := range []string{
		"NEW id; never reuse a lost store's id", // a reused id inherits the old stream
		"out of band, never through the relay or by printing keys",
		"directory 0700, files 0600",
		"stop any daemon in the new project before cloning",
		"Do not attach before or after clone",
	} {
		require.Contains(t, text, phrase)
	}
	// The sequence opens with the tick on the existing peer, then the export,
	// and the clone and the closing tick come last.
	tick := strings.Index(text, "`mtix sync relay tick`")
	export := strings.Index(text, "`mtix sync relay clone --export`")
	clone := strings.Index(text, "Run `mtix sync relay clone`")
	closing := strings.LastIndex(text, "`mtix sync relay tick`")
	require.True(t, tick >= 0 && tick < export && export < clone && clone < closing, text)
}
