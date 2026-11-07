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
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// relayPeerCLI makes a project on its own peer id (sync.relay.peer_id gives each
// store its own, so two peers run on one machine) on a shared relay.
func relayPeerCLI(t *testing.T, binary, relayDir, peer string) quarantineRecoveryCLI {
	t.Helper()
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".mtix"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".mtix", "config.yaml"), []byte("prefix: TEST\nmax_depth: 10\n"), 0o644))
	home := t.TempDir()
	f := quarantineRecoveryCLI{binary: binary, project: project}
	f.env = append(hostguard.Environ(), "MTIX_AUTHOR_ID="+peer, "XDG_CONFIG_HOME="+home, "APPDATA="+home)
	// A relay peer id is 16 hex characters; the label names the peer in the author id.
	peerID := strings.Repeat(strings.TrimPrefix(peer, "peer-"), 16)
	for _, kv := range [][]string{{"sync.relay.dir", relayDir}, {"sync.relay.peer_id", peerID}} {
		mustRun(t, f, "config", "set", kv[0], kv[1])
	}
	return f
}

// TestImportMerge_RelaySnapshotClone_RealBinary_KeepsUIDNamesWhatStaysAndPrintsNoBackfillRoute
// (round 5, F49 and F55): on a real relay pair, a clone made from the
// publisher's snapshot merges a file that retitles a task and carries another
// uid for it. The uid is kept and named, the retitle is named unsent, no
// backfill route is printed on a relay board, and the ticks neither quarantine
// nor wedge.
func TestImportMerge_RelaySnapshotClone_RealBinary_KeepsUIDNamesWhatStaysAndPrintsNoBackfillRoute(t *testing.T) {
	binary := buildQuarantineRecoveryCLI(t)
	relayDir := t.TempDir()
	pub := relayPeerCLI(t, binary, relayDir, "peer-a")
	mustRun(t, pub, "create", "Seed")
	mustRun(t, pub, "sync", "relay", "init", relayDir, "--no-auth")
	mustRun(t, pub, "sync", "relay", "tick")
	mustRun(t, pub, "sync", "relay", "clone", "--export")
	clone := relayPeerCLI(t, binary, relayDir, "peer-b")
	mustRun(t, clone, "sync", "relay", "clone")
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal([]byte(mustRun(t, clone, "export")), &data))
	other, err := model.NewBackfillUID() // another uid for the same creation time: the same task
	require.NoError(t, err)
	data.Nodes[0].UID = other
	data.Nodes[0].Title = "Retitled on the clone"
	data.Nodes[0].ContentHash = model.ComputeContentHash(data.Nodes[0].Title, "", "", "", nil)
	require.NoError(t, sqlite.RecomputeExportChecksum(&data))
	raw, err := json.Marshal(&data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))

	out := mustRun(t, clone, "import", path, "--mode", "merge", "--force")

	require.Contains(t, out, "TEST-1")
	require.Contains(t, out, "uid", "the kept uid is named")
	require.NotContains(t, out, "mtix sync backfill sends it", "a relay board has no backfill route")
	for _, c := range []quarantineRecoveryCLI{clone, pub, clone} {
		mustRun(t, c, "sync", "relay", "tick")
	}
	require.Contains(t, mustRun(t, pub, "list"), "Seed", "the publisher still holds its own title: the value stayed local")
	require.NotContains(t, mustRun(t, pub, "list"), "Retitled on the clone")
}

// TestSyncBackfill_RelayBoard_RealBinary_RefusesWithTheRelayTextAndDryRunStillReads
// (round 6 ruling 5): the backfill refusal and the import line share one
// predicate, so on a relay board the real command refuses and says why, and
// --dry-run, which writes nothing, is not refused.
func TestSyncBackfill_RelayBoard_RealBinary_RefusesWithTheRelayTextAndDryRunStillReads(t *testing.T) {
	binary := buildQuarantineRecoveryCLI(t)
	pub := relayPeerCLI(t, binary, t.TempDir(), "peer-a")
	mustRun(t, pub, "create", "Seed")

	out := pub.run(t, "sync", "backfill")

	require.NotZero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	require.Contains(t, out.stdout+out.stderr, "exchanges events through a relay")
	require.Contains(t, out.stdout+out.stderr, "there is no backfill route on a relay board")
	dry := pub.run(t, "sync", "backfill", "--dry-run")
	require.NotContains(t, dry.stdout+dry.stderr, "exchanges events through a relay")
}
