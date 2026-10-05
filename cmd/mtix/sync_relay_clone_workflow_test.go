// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/bootstrap"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// claimedExporter exports the local store with its first task claimed, as a
// rejoining peer's snapshot would carry a teammate's claim.
type claimedExporter struct{}

func (claimedExporter) Export(ctx context.Context, project, version string) (*sqlite.ExportData, error) {
	data, err := app.store.Export(ctx, project, version)
	if err != nil {
		return nil, err
	}
	data.Nodes[0].Status, data.Nodes[0].Assignee = string(model.StatusInProgress), "bob"
	if err := sqlite.RecomputeExportChecksum(data); err != nil {
		return nil, err
	}
	return data, nil
}

// TestRunRelayCloneImport_WorkflowConflict_RefusesThenResolves verifies a
// rejoining peer whose snapshot differs from its store in a task's status
// and assignee is refused with the conflict listed (nothing written), and
// that --prefer, --theirs and --ours settle it (MTIX-95.31.13).
func TestRunRelayCloneImport_WorkflowConflict_RefusesThenResolves(t *testing.T) {
	tests := []struct {
		name       string
		flags      importFlags
		wantStatus model.Status
	}{
		{"prefer theirs", importFlags{prefer: "theirs"}, model.StatusInProgress},
		{"prefer ours", importFlags{prefer: "ours"}, model.StatusOpen},
		{"theirs list", importFlags{theirs: []string{"TEST-1"}}, model.StatusInProgress},
		{"ours list", importFlags{ours: []string{"TEST-1"}}, model.StatusOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Local task", "", "", 3, "", "", "", "", ""))
			relayDir := t.TempDir()
			_, err := bootstrap.ExportSnapshot(t.Context(), bootstrap.ExportRequest{
				Store: claimedExporter{}, RelayDir: relayDir, Project: "TEST", ExportedBy: "0123456789abcdef",
				CreatedAt: time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC),
				Positions: map[string]uint64{"0123456789abcdef": 1},
			})
			require.NoError(t, err)
			var stderr bytes.Buffer
			cmd := &cobra.Command{Use: "clone"}
			cmd.SetErr(&stderr)
			cmd.SetOut(&bytes.Buffer{})

			err = runRelayCloneImport(t.Context(), cmd, relayDir, importFlags{})
			require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
			assert.Contains(t, stderr.String(), "WORKFLOW CONFLICTS: 1 task(s)")
			n, err := app.store.GetNode(t.Context(), "TEST-1")
			require.NoError(t, err)
			assert.Equal(t, model.StatusOpen, n.Status, "a refusal writes nothing")

			require.NoError(t, runRelayCloneImport(t.Context(), cmd, relayDir, tt.flags))
			n, err = app.store.GetNode(t.Context(), "TEST-1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, n.Status)
		})
	}
}

// TestNewRelayCloneCmd_WorkflowFlags_AreRegistered verifies relay clone
// carries --prefer, --theirs and --ours.
func TestNewRelayCloneCmd_WorkflowFlags_AreRegistered(t *testing.T) {
	cmd := newRelayCloneCmd()
	for _, name := range []string{"prefer", "theirs", "ours"} {
		require.NotNil(t, cmd.Flags().Lookup(name), name)
	}
}
