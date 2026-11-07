//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// retitleNode sets a node's title and content hash in an exported board.
func retitleNode(data *sqlite.ExportData, id, title string) {
	for i := range data.Nodes {
		if data.Nodes[i].ID == id {
			data.Nodes[i].Title = title
			data.Nodes[i].ContentHash = model.ComputeContentHash(title, "", "", "", nil)
		}
	}
}

// TestImportMerge_PartialLog_RealBinary_PrintedBackfillReachesAFreshClone is
// the round 4 ruling 2 run: a task made before any hub has no create in the
// log; after sync init and one create that is pushed, a merge with a change
// and a child journals nothing for that task or its child and names them;
// the printed route (mtix sync backfill) sends just those, and a fresh clone
// holds every task with the merged values.
func TestImportMerge_PartialLog_RealBinary_PrintedBackfillReachesAFreshClone(t *testing.T) {
	dsn := requireCmdPG(t)
	hub := openCmdHub(t)
	binary := buildQuarantineRecoveryCLI(t)
	initTestApp(t)
	operator := operatorTestHome(t)
	var env []string
	for _, kv := range hostguard.Environ() {
		if !strings.HasPrefix(kv, transport.EnvDSN+"=") {
			env = append(env, kv)
		}
	}
	env = append(env, "MTIX_AUTHOR_ID=replica-op", "XDG_CONFIG_HOME="+operator, "APPDATA="+operator)
	f := quarantineRecoveryCLI{binary: binary, project: filepath.Dir(app.mtixDir), env: env, hub: hub}
	out := f.run(t, "create", "Made before any hub")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	_, err := app.store.WriteDB().Exec(`DELETE FROM sync_events`) // what an unsynced store holds: no create row
	require.NoError(t, err)
	f.env = append(f.env, transport.EnvDSN+"="+dsn)
	for _, args := range [][]string{{"sync", "init", "--insecure-tls"}, {"create", "Made after the hub"}, {"sync", "push", "--insecure-tls"}} {
		out = f.run(t, args...)
		require.Zero(t, out.exitCode, "%v: %s%s", args, out.stdout, out.stderr)
	}

	out = f.run(t, "export")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &data))
	retitleNode(&data, "TEST-1", "Merged unsent")
	retitleNode(&data, "TEST-2", "Merged sent")
	child := data.Nodes[0]
	child.ID, child.ParentID, child.Depth, child.Seq = "TEST-1.1", "TEST-1", 1, 1
	child.UID, child.Title = clock.MustNewEventID(), "Child under the unsent task"
	child.NodeType = string(model.NodeTypeForDepth(1))
	child.ContentHash = model.ComputeContentHash(child.Title, "", "", "", nil)
	child.Annotations, child.Activity = nil, nil
	data.Nodes = append(data.Nodes, child)
	data.NodeCount = len(data.Nodes)
	require.NoError(t, sqlite.RecomputeExportChecksum(&data))
	raw, err := json.Marshal(&data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	out = f.run(t, "import", path, "--mode", "merge", "--force")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	require.Contains(t, out.stdout, "have no creation in this sync log")
	require.Contains(t, out.stdout, "TEST-1")
	require.Contains(t, out.stdout, "mtix sync backfill")
	var unsent int
	require.NoError(t, app.store.WriteDB().QueryRow(
		`SELECT COUNT(*) FROM sync_events WHERE node_id IN ('TEST-1', 'TEST-1.1')`).Scan(&unsent))
	require.Zero(t, unsent, "no event for the unsent task or its child")

	for _, args := range [][]string{{"sync", "backfill"}, {"sync", "push", "--insecure-tls"}} { // the printed route
		out = f.run(t, args...)
		require.Zero(t, out.exitCode, "%v: %s%s", args, out.stdout, out.stderr)
	}
	clone := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(clone, ".mtix"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(clone, ".mtix", "config.yaml"), []byte("prefix: TEST\nmax_depth: 10\n"), 0o644))
	fresh := quarantineRecoveryCLI{binary: binary, project: clone, env: f.env, hub: hub}
	out = fresh.run(t, "sync", "clone", "--insecure-tls")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	out = fresh.run(t, "list")
	for _, want := range []string{"Merged unsent", "Merged sent", "Child under the unsent task"} {
		require.Contains(t, out.stdout, want, "the fresh clone holds the task")
	}
}
