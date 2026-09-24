// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// initTestAppWithAuthorConfig is initTestApp with an author_id config key
// ("" writes none), so a test can drive the configured process identity.
func initTestAppWithAuthorConfig(t *testing.T, authorID string) {
	t.Helper()
	saveAndResetApp(t)
	tmpDir := t.TempDir()
	mtixDir := filepath.Join(tmpDir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	config := "prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"
	if authorID != "" {
		config += "author_id: " + authorID + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "config.yaml"), []byte(config), 0o644))
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	require.NoError(t, os.Chdir(tmpDir))
	require.NoError(t, initApp(&cobra.Command{Use: "test"}, ""))
	t.Cleanup(func() {
		if app.store != nil {
			_ = app.store.Close()
		}
	})
}

// TestRegisterMCPTools_DeferAuthor_IsConfiguredIdentity verifies the wiring
// `mtix mcp` uses: mtix_defer records the process identity when one is
// configured (MTIX_AUTHOR_ID, else the author_id config key) on its activity
// entry and its sync event, and "mcp" when none is configured, not the CLI's
// "cli" fallback (MTIX-95.22 round 2).
func TestRegisterMCPTools_DeferAuthor_IsConfiguredIdentity(t *testing.T) {
	tests := []struct {
		name   string
		env    string
		config string
		want   string
	}{
		{"MTIX_AUTHOR_ID set", "agent-9", "", "agent-9"},
		{"env wins over config", "agent-9", "cfg-agent", "agent-9"},
		{"author_id config only", "", "cfg-agent", "cfg-agent"},
		{"nothing configured", "", "", "mcp"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(sqlite.AuthorIDEnv, tt.env)
			initTestAppWithAuthorConfig(t, tt.config)
			require.NoError(t, runCreate("Defer via MCP", "", "", 3, "", "", "", "", ""))
			reg := mcp.NewToolRegistry()

			registerMCPTools(reg)

			ctx := context.Background()
			_, err := reg.Call(ctx, "mtix_defer", json.RawMessage(`{"id":"TEST-1"}`))
			require.NoError(t, err)
			entries, err := app.store.GetActivity(ctx, "TEST-1", 100, 0)
			require.NoError(t, err)
			require.NotEmpty(t, entries)
			assert.Equal(t, tt.want, entries[len(entries)-1].Author, "activity author")
			var author string
			require.NoError(t, app.store.QueryRow(ctx,
				`SELECT author_id FROM sync_events WHERE node_id = ? AND op_type = ?`,
				"TEST-1", string(model.OpTransitionStatus)).Scan(&author))
			assert.Equal(t, tt.want, author, "sync event author")
		})
	}
}
