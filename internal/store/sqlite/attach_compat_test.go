// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"crypto/sha256"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The released CLI must preserve local coordination state through ordinary operations.
func TestAttachSchema_054BinaryOpensMigratedStore(t *testing.T) {
	binary := compat054Binary(t)
	project := t.TempDir()
	dir := filepath.Join(project, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("prefix: TEST\nsync:\n  auto_sync: false\n"), 0600))
	path := filepath.Join(dir, "data", "mtix.db")
	s, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	attachSeedLocalState(t, s)
	before := attachLocalState(t, s)
	require.NoError(t, s.Close())
	attachReaderCommand(t, binary, project, "", "list", "--json")
	attachReaderCommand(t, binary, project, "", "create", "local compatibility node")
	reopened, err := sqlite.New(path, slog.Default())
	require.NoError(t, err)
	var count int
	require.NoError(t, reopened.ReadDB().QueryRow("SELECT count(*) FROM nodes WHERE title=?", "local compatibility node").Scan(&count))
	assert.Equal(t, 1, count)
	require.NoError(t, reopened.Close())
	if dsn := os.Getenv("MTIX_ATTACH_TEST_DSN"); dsn != "" {
		attachReaderPull(t, binary, project, path, dsn)
	}
	reopened, err = sqlite.New(path, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	assert.Equal(t, before, attachLocalState(t, reopened))
}

func attachReaderCommand(t *testing.T, binary, project, dsn string, args ...string) []byte {
	t.Helper()
	raw, err := os.ReadFile(binary)
	require.NoError(t, err)
	t.Logf("released reader before execution commit=%s sha256=%x", compat054TagCommit, sha256.Sum256(raw))
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = project
	cmd.Env = append(os.Environ(), "MTIX_SYNC_DSN="+dsn, "MTIX_SYNC_HOOK=")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "released reader %v: %s", args, output)
	t.Logf("released reader pid=%d exit=%d args=%v", cmd.Process.Pid, cmd.ProcessState.ExitCode(), args)
	return output
}

func attachSeedLocalState(t *testing.T, s *sqlite.Store) {
	t.Helper()
	for _, query := range []string{
		"INSERT INTO agent_registrations(agent_id,harness,uid,host_id,origin,registered_at,last_seen,registrar_version,attach_protocol,returned_seq_mark) VALUES('worker','local','uid','host','manual','now','now','v',1,7)",
		"INSERT INTO wake_ledger VALUES('worker','host','uid',7,1,'now','claimed')",
		"INSERT INTO wake_scan_cursor VALUES('host','uid',7)",
		"INSERT INTO dispatcher_heartbeat VALUES('host','uid',123,1,'v','now')",
		"INSERT INTO board_flags VALUES('flag','kept','now','local')",
		"INSERT INTO attach_pending(kind,board_key,path,payload,queued_at) VALUES('registration','board','project','kept','now')",
		"INSERT INTO wake_leases(agent_id,host_id,uid,holder,expires_at,high_water,dirty,acquired_at) VALUES('worker','host','uid','holder','later',7,0,'now')",
		"INSERT OR REPLACE INTO meta(key,value) VALUES('min_dispatch_protocol','3')",
	} {
		_, err := s.WriteDB().Exec(query)
		require.NoError(t, err)
	}
}

func attachLocalState(t *testing.T, s *sqlite.Store) map[string]string {
	t.Helper()
	state := map[string]string{}
	for _, query := range []string{
		"SELECT * FROM agent_registrations", "SELECT * FROM wake_ledger", "SELECT * FROM wake_scan_cursor",
		"SELECT * FROM dispatcher_heartbeat", "SELECT * FROM board_flags", "SELECT * FROM attach_pending", "SELECT * FROM wake_leases",
		"SELECT key,value FROM meta WHERE key IN ('store_instance_id','min_dispatch_protocol','schema_version') ORDER BY key",
	} {
		rows, err := s.ReadDB().Query(query)
		require.NoError(t, err)
		defer func() { require.NoError(t, rows.Close()) }()
		columns, err := rows.Columns()
		require.NoError(t, err)
		var all [][]any
		for rows.Next() {
			values := make([]any, len(columns))
			dest := make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			require.NoError(t, rows.Scan(dest...))
			all = append(all, values)
		}
		require.NoError(t, rows.Err())
		raw, err := json.Marshal(all)
		require.NoError(t, err)
		state[query] = string(raw)
	}
	return state
}
