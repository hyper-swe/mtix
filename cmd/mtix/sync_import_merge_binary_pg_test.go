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

// MTIX-112.17.16.16 round 3: the hub number collision on the real binary and
// a real hub. Every round-2 finding escaped tests that called the command
// functions in process.

func TestImportMerge_HubNumberCollision_RealBinary_PushRenumbersAndDelivers(t *testing.T) {
	_ = requireCmdPG(t)
	f := newQuarantineRecoveryCLI(t, buildQuarantineRecoveryCLI(t))
	out := f.run(t, "create", "Seed")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	out = f.run(t, "sync", "push", "--insecure-tls")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	theirs := clock.MustNewEventID()
	payload, err := model.EncodePayload(&model.CreateNodePayload{Title: "Teammate task", NodeType: model.NodeTypeForDepth(0)})
	require.NoError(t, err)
	pushToHub(t, f.hub, &model.SyncEvent{EventID: theirs, UID: theirs, ProjectPrefix: "TEST", NodeID: "TEST-2",
		OpType: model.OpCreateNode, Payload: payload, WallClockTS: 1000, LamportClock: 5,
		VectorClock: model.VectorClock{"remote-author": 5}, AuthorID: "remote-author", AuthorMachineHash: "ffffffffffffffff"})

	out = f.run(t, "export")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &data))
	mine := clock.MustNewEventID()
	extra := data.Nodes[0]
	extra.ID, extra.Seq, extra.UID, extra.Title = "TEST-2", 2, mine, "Merged task"
	extra.ContentHash = model.ComputeContentHash("Merged task", "", "", "", nil)
	data.Nodes = append(data.Nodes, extra)
	data.NodeCount = len(data.Nodes)
	require.NoError(t, sqlite.RecomputeExportChecksum(&data))
	raw, err := json.Marshal(&data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	out = f.run(t, "import", path, "--mode", "merge", "--force")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	out = f.run(t, "sync", "push", "--insecure-tls")

	require.Zero(t, out.exitCode, "the push is not wedged by the number: %s%s", out.stdout, out.stderr)
	events, _, err := f.hub.PullEvents(t.Context(), transport.PullCursor{}, 1000)
	require.NoError(t, err)
	reached := false
	for _, e := range events {
		reached = reached || (e.OpType == model.OpCreateNode && e.UID == mine)
	}
	require.True(t, reached, "the merged task reached the hub")
}

// TestImportMerge_StoreWithTasksAndNoJournal_RealBinary_BackfillStillReachesAFreshClone:
// a store filled before any sync has tasks and an empty journal (what an
// unsynced merge leaves). With the hub connection string exported, a merge
// journals nothing and says backfill sends the tasks; backfill then runs, and
// a fresh clone holds the task.
func TestImportMerge_StoreWithTasksAndNoJournal_RealBinary_BackfillStillReachesAFreshClone(t *testing.T) {
	dsn := requireCmdPG(t)
	hub := openCmdHub(t)
	binary := buildQuarantineRecoveryCLI(t)
	initTestApp(t)
	operator := operatorTestHome(t)
	var env []string
	for _, kv := range hostguard.Environ() {
		if !strings.HasPrefix(kv, transport.EnvDSN+"=") {
			env = append(env, kv) // no hub evidence yet
		}
	}
	env = append(env, "MTIX_AUTHOR_ID=replica-op", "XDG_CONFIG_HOME="+operator, "APPDATA="+operator)
	f := quarantineRecoveryCLI{binary: binary, project: filepath.Dir(app.mtixDir), env: env, hub: hub}
	out := f.run(t, "create", "Filled before any hub")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	_, err := app.store.WriteDB().Exec(`DELETE FROM sync_events`) // the journal an unsynced merge leaves
	require.NoError(t, err)
	out = f.run(t, "export")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &data))
	data.Nodes[0].Title = "Retitled by the merge"
	data.Nodes[0].ContentHash = model.ComputeContentHash(data.Nodes[0].Title, data.Nodes[0].Description, data.Nodes[0].Prompt, data.Nodes[0].Acceptance, nil)
	require.NoError(t, sqlite.RecomputeExportChecksum(&data))
	raw, err := json.Marshal(&data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	f.env = append(f.env, transport.EnvDSN+"="+dsn) // the exported hub connection string
	out = f.run(t, "import", path, "--mode", "merge", "--force")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	require.Contains(t, out.stdout, "mtix sync backfill")
	var journal int
	require.NoError(t, app.store.WriteDB().QueryRow(`SELECT COUNT(*) FROM sync_events`).Scan(&journal))
	require.Zero(t, journal, "the merge journaled nothing")

	for _, args := range [][]string{{"sync", "init", "--insecure-tls"}, {"sync", "backfill"}, {"sync", "push", "--insecure-tls"}} {
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
	require.Contains(t, out.stdout, "Retitled by the merge", "the fresh clone holds the task")
}
