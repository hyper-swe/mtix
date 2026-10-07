// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Agent identity boundaries reject unsafe nonempty IDs without writes (MTIX-125).
package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

func invalidAgentIDs() []struct{ name, id string } {
	return []struct{ name, id string }{
		{"spaces", "   "}, {"unicode_space", "\u2003\u00a0"},
		{"overlong", strings.Repeat("a", 65)}, {"utf8_bytes", strings.Repeat("é", 33)},
		{"nul", "a\x00b"}, {"tab", "a\tb"}, {"newline", "a\nb"},
		{"del", "a\x7fb"}, {"c1", "a\u0085b"},
		{"zero_width", "a\u200bb"}, {"joiner", "a\u200db"},
		{"bidi", "a\u202eb"}, {"bidi_isolate", "a\u2066b"},
		{"bom", "a\ufeffb"}, {"soft_hyphen", "a\u00adb"},
	}
}

func TestAgentID_CLIBoundaries_RejectWithoutMutation(t *testing.T) {
	for _, op := range []string{"create", "claim", "update"} {
		t.Run(op, func(t *testing.T) {
			for _, tc := range invalidAgentIDs() {
				t.Run(tc.name, func(t *testing.T) {
					initTestApp(t)
					ctx := context.Background()
					n, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "existing"})
					require.NoError(t, err)
					before, err := app.store.GetNode(ctx, n.ID)
					require.NoError(t, err)
					var cmd *cobra.Command
					var args []string
					switch op {
					case "create":
						cmd = newCreateCmd()
						args = []string{"refused", "--assign", tc.id}
					case "claim":
						cmd = newClaimCmd()
						args = []string{n.ID, "--agent", tc.id}
					default:
						cmd = newUpdateCmd()
						args = []string{n.ID, "--assignee", tc.id}
					}
					cmd.SetArgs(args)
					err = cmd.Execute()
					require.ErrorIs(t, err, model.ErrInvalidInput)
					require.Contains(t, err.Error(), "agent ID")
					after, err := app.store.GetNode(ctx, n.ID)
					require.NoError(t, err)
					require.Equal(t, before, after)
				})
			}
		})
	}
}

func TestAgentID_CLIUpdateEmpty_ClearsAssignment(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	n, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "existing", Assignee: "worker"})
	require.NoError(t, err)
	cmd := newUpdateCmd()
	cmd.SetArgs([]string{n.ID, "--assignee", ""})
	require.NoError(t, cmd.Execute())
	got, err := app.store.GetNode(ctx, n.ID)
	require.NoError(t, err)
	require.Empty(t, got.Assignee)
	keep := "kept"
	require.NoError(t, app.nodeSvc.UpdateNode(ctx, n.ID, &store.NodeUpdate{Assignee: &keep}))
	cmd = newUpdateCmd()
	cmd.SetArgs([]string{n.ID, "--title", "changed"})
	require.NoError(t, cmd.Execute())
	got, err = app.store.GetNode(ctx, n.ID)
	require.NoError(t, err)
	require.Equal(t, keep, got.Assignee)
}

func TestAgentID_CLIHelp_ExplainsContract(t *testing.T) {
	for _, tc := range []struct {
		cmd         *cobra.Command
		flag, empty string
	}{{newCreateCmd(), "assign", "empty"}, {newUpdateCmd(), "assignee", "clear"}, {newClaimCmd(), "agent", "64"}} {
		usage := tc.cmd.Flags().Lookup(tc.flag).Usage
		require.Contains(t, usage, "64 UTF-8 bytes")
		require.Contains(t, usage, "whitespace-only")
		require.Contains(t, usage, "control")
		require.Contains(t, usage, "format")
		require.Contains(t, usage, tc.empty)
	}
}

type agentIDCLIEvents struct{ events []service.Event }

func (b *agentIDCLIEvents) Broadcast(_ context.Context, e service.Event) error {
	b.events = append(b.events, e)
	return nil
}

func TestAgentID_CLIClaimUnclaim_UseServiceAndFixedAuthor(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	bus := &agentIDCLIEvents{}
	app.nodeSvc = service.NewNodeService(app.store, bus, nil, nil, time.Now)
	n, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "existing"})
	require.NoError(t, err)
	bus.events = nil
	require.NoError(t, runClaim(n.ID, "worker"))
	require.Len(t, bus.events, 1)
	require.Equal(t, service.EventNodeClaimed, bus.events[0].Type)
	require.NoError(t, runUnclaim(n.ID, "release"))
	require.Len(t, bus.events, 2)
	require.Equal(t, "cli", bus.events[1].Author)
}

func TestAgentID_CLIValidRawAndEmptyCreate_Preserved(t *testing.T) {
	for _, agent := range []string{"", " Agent.User+é ", strings.Repeat("a", 64), strings.Repeat("é", 32)} {
		t.Run(agent, func(t *testing.T) {
			initTestApp(t)
			cmd := newCreateCmd()
			cmd.SetArgs([]string{"raw", "--assign", agent})
			require.NoError(t, cmd.Execute())
			n, err := app.store.GetNode(context.Background(), "TEST-1")
			require.NoError(t, err)
			require.Equal(t, agent, n.Assignee)
			if agent != "" {
				require.NoError(t, runUnclaim(n.ID, "release"))
				claim := newClaimCmd()
				claim.SetArgs([]string{n.ID, "--agent", agent})
				require.NoError(t, claim.Execute())
				got, err := app.store.GetNode(context.Background(), n.ID)
				require.NoError(t, err)
				require.Equal(t, agent, got.Assignee)
			}
		})
	}
}
