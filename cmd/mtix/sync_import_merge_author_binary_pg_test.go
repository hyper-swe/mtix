//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// cloneOf makes a second replica of the project f syncs, from the hub, under
// its own author id (a teammate's machine).
func cloneOf(t *testing.T, f quarantineRecoveryCLI, author string) quarantineRecoveryCLI {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".mtix"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".mtix", "config.yaml"), []byte("prefix: TEST\nmax_depth: 10\n"), 0o644))
	c := quarantineRecoveryCLI{binary: f.binary, project: dir, env: append(slices.Clone(f.env), "MTIX_AUTHOR_ID="+author), hub: f.hub}
	out := c.run(t, "sync", "clone", "--insecure-tls")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	return c
}

func mustRun(t *testing.T, c quarantineRecoveryCLI, args ...string) string {
	t.Helper()
	out := c.run(t, args...)
	require.Zero(t, out.exitCode, "%v: %s%s", args, out.stdout, out.stderr)
	return out.stdout
}

// TestImportMerge_AuthorPushesLater_RealBinary_BothOrdersLeaveEveryReplicaWithTheAuthorsValues
// is the round 5 ruling 1 run: Bob creates a task and edits it before he
// pushes; Alice merges Bob's export; in either push order nothing is held on
// either side, doctor is clean, and Alice, Bob and a fresh clone end with
// Bob's latest values.
func TestImportMerge_AuthorPushesLater_RealBinary_BothOrdersLeaveEveryReplicaWithTheAuthorsValues(t *testing.T) {
	for _, alicePushesFirst := range []bool{true, false} {
		name := "bob pushes first"
		if alicePushesFirst {
			name = "alice pushes first"
		}
		t.Run(name, func(t *testing.T) {
			_ = requireCmdPG(t)
			alice := newQuarantineRecoveryCLI(t, buildQuarantineRecoveryCLI(t))
			mustRun(t, alice, "create", "Seed")
			mustRun(t, alice, "sync", "push", "--insecure-tls")
			bob := cloneOf(t, alice, "bob")
			mustRun(t, bob, "create", "Bob task")
			mustRun(t, bob, "update", "TEST-2", "--title", "Bob edited once")
			var data sqlite.ExportData
			require.NoError(t, json.Unmarshal([]byte(mustRun(t, bob, "export")), &data))
			path := filepath.Join(t.TempDir(), "bob.json")
			raw, err := json.Marshal(&data)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(path, raw, 0o600))

			merged := mustRun(t, alice, "import", path, "--mode", "merge", "--force")
			require.Contains(t, merged, "TEST-2")
			require.Contains(t, merged, "stay unsent")
			mustRun(t, bob, "update", "TEST-2", "--title", "Bob edited again")
			if alicePushesFirst {
				mustRun(t, alice, "sync", "push", "--insecure-tls")
				mustRun(t, bob, "sync", "push", "--insecure-tls")
			} else {
				mustRun(t, bob, "sync", "push", "--insecure-tls")
				mustRun(t, alice, "sync", "push", "--insecure-tls")
			}
			mustRun(t, alice, "sync", "pull", "--insecure-tls")
			mustRun(t, bob, "sync", "pull", "--insecure-tls")

			fresh := cloneOf(t, alice, "carol")
			for who, c := range map[string]quarantineRecoveryCLI{"alice": alice, "bob": bob, "fresh clone": fresh} {
				list := mustRun(t, c, "list")
				require.Contains(t, list, "Bob edited again", who)
				require.False(t, strings.Contains(list, "Bob edited once"), who)
			}
			alice.doctor(t, 0)
			bob.doctor(t, 0)
		})
	}
}

func statusOfTask(t *testing.T, c quarantineRecoveryCLI, id string) string {
	t.Helper()
	var node struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(mustRun(t, c, "show", id, "--json")), &node))
	return node.Status
}

// TestImportMerge_BlockerResolvedByTheFile_RealBinary_EveryReplicaReleasesTheDependent
// is the round 5 ruling 2 run: the blocker is open and its dependent blocked
// everywhere; Bob resolves the blocker, so his board shows the dependent
// open; Alice merges Bob's export with --prefer theirs. Alice, Bob, the hub's
// fresh clone and a later pull agree that the dependent is released.
func TestImportMerge_BlockerResolvedByTheFile_RealBinary_EveryReplicaReleasesTheDependent(t *testing.T) {
	_ = requireCmdPG(t)
	alice := newQuarantineRecoveryCLI(t, buildQuarantineRecoveryCLI(t))
	mustRun(t, alice, "create", "Seed")
	mustRun(t, alice, "create", "Blocker")
	mustRun(t, alice, "create", "Dependent")
	mustRun(t, alice, "dep", "add", "TEST-2", "TEST-3")
	require.Equal(t, "blocked", statusOfTask(t, alice, "TEST-3"))
	mustRun(t, alice, "sync", "push", "--insecure-tls")
	bob := cloneOf(t, alice, "bob")
	mustRun(t, bob, "claim", "TEST-2", "--agent", "bob")
	mustRun(t, bob, "done", "TEST-2")
	require.Equal(t, "open", statusOfTask(t, bob, "TEST-3"), "Bob's board shows the dependent released")
	path := filepath.Join(t.TempDir(), "bob.json")
	require.NoError(t, os.WriteFile(path, []byte(mustRun(t, bob, "export")), 0o600))

	mustRun(t, alice, "import", path, "--mode", "merge", "--prefer", "theirs", "--force")
	require.Equal(t, "open", statusOfTask(t, alice, "TEST-3"), "Alice releases it too")
	mustRun(t, alice, "sync", "push", "--insecure-tls")
	mustRun(t, bob, "sync", "push", "--insecure-tls")
	mustRun(t, alice, "sync", "pull", "--insecure-tls")
	mustRun(t, bob, "sync", "pull", "--insecure-tls")

	fresh := cloneOf(t, alice, "carol")
	want := statusOfTask(t, bob, "TEST-3")
	for who, c := range map[string]quarantineRecoveryCLI{"alice": alice, "bob": bob, "fresh clone": fresh} {
		require.Equal(t, want, statusOfTask(t, c, "TEST-3"), who)
		require.Equal(t, "done", statusOfTask(t, c, "TEST-2"), who)
	}
	alice.doctor(t, 0)
}
