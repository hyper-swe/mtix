// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// This test executes the actual tagged CLI against a populated additive lease store.

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestSchema_WakeLeasesAdditive_054BinaryOpensStore(t *testing.T) {
	binary := compat054Binary(t)
	project := t.TempDir()
	mtix := filepath.Join(project, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(mtix, "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(mtix, "config.yaml"), []byte("prefix: TEST\n"), 0600))
	path := filepath.Join(mtix, "data", "mtix.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	s.SetClock(wakeTime)
	lease, err := s.AcquireWakeLease(t.Context(), wakeKey(), "", 15*time.Minute)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	for _, args := range [][]string{{"list", "--json"}, {"inbox", "--agent", wakeKey().AgentID}} {
		cmd := exec.CommandContext(t.Context(), binary, args...)
		cmd.Dir = project
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "real reader %v: %s", args, output)
		assert.NotContains(t, string(output), "schema version")
	}
	reopened, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	var holder, version string
	var mark, dirty int64
	db := newTestDB(t, path)
	require.NoError(t, db.QueryRow("SELECT holder,high_water,dirty FROM wake_leases WHERE agent_id=? AND host_id=? AND uid=?", wakeKey().AgentID, wakeKey().HostID, wakeKey().UID).Scan(&holder, &mark, &dirty))
	assert.Equal(t, lease.Holder, holder)
	assert.Equal(t, lease.HighWater, mark)
	assert.Zero(t, dirty)
	require.NoError(t, db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&version))
	assert.Equal(t, "4", version)
}
