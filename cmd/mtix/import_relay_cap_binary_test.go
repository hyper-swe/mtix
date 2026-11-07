//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// MTIX-112.17.16.16 round 3: the queue cap on a relay-only board, on the real
// binary. The events of a relay board stay pending, so the printed fix must be
// the config command, it must clear the refusal, and a refused merge must not
// take a backup that rotates the older recovery points away.

func relayBoardCLI(t *testing.T) quarantineRecoveryCLI {
	t.Helper()
	binary := buildQuarantineRecoveryCLI(t)
	initTestApp(t)
	operator := operatorTestHome(t)
	f := quarantineRecoveryCLI{binary: binary, project: filepath.Dir(app.mtixDir)}
	f.env = append(hostguard.Environ(), "MTIX_AUTHOR_ID=relay-op", "XDG_CONFIG_HOME="+operator, "APPDATA="+operator)
	out := f.run(t, "create", "Seed")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	relay := t.TempDir()
	out = f.run(t, "sync", "relay", "init", relay, "--no-auth")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	out = f.run(t, "config", "set", "sync.relay.dir", relay)
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	return f
}

func retitledBoardFile(t *testing.T, f quarantineRecoveryCLI, title string) string {
	t.Helper()
	out := f.run(t, "export")
	require.Zero(t, out.exitCode, "%s", out.stderr)
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal([]byte(out.stdout), &data))
	data.Nodes[0].Title = title
	data.Nodes[0].ContentHash = model.ComputeContentHash(title, data.Nodes[0].Description, data.Nodes[0].Prompt, data.Nodes[0].Acceptance, nil)
	require.NoError(t, sqlite.RecomputeExportChecksum(&data))
	raw, err := json.Marshal(&data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

func backupCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(app.mtixDir, "data", "backups"))
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return len(entries)
}

func TestImportMerge_RelayOnlyBoard_CapRefusalFixClearsItAndTakesNoBackup(t *testing.T) {
	f := relayBoardCLI(t)
	out := f.run(t, "config", "set", "sync.max_queue_size", "1")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	path := retitledBoardFile(t, f, "Imported title")
	before := backupCount(t)

	for i := 0; i < 7; i++ { // seven refusals must not rotate the recovery points
		out = f.run(t, "import", path, "--mode", "merge", "--force")
		require.NotZero(t, out.exitCode, "the cap refuses: %s%s", out.stdout, out.stderr)
	}

	require.Equal(t, before, backupCount(t), "a refused merge takes no backup")
	fix := regexp.MustCompile(`mtix config set (sync\.max_queue_size) (\d+)`).FindStringSubmatch(out.stderr + out.stdout)
	require.NotNil(t, fix, "%s%s", out.stdout, out.stderr)
	out = f.run(t, "config", "set", fix[1], fix[2])
	require.Zero(t, out.exitCode, "the printed fix: %s%s", out.stdout, out.stderr)
	out = f.run(t, "import", path, "--mode", "merge", "--force")
	require.Zero(t, out.exitCode, "the same import runs after the fix: %s%s", out.stdout, out.stderr)
	require.Contains(t, out.stdout, "Journaled")
	require.True(t, strings.Contains(out.stdout, "relay") && !strings.Contains(out.stdout, "sends them to the hub"),
		"the line names the relay, not a hub: %s", out.stdout)
}

func TestImportMerge_InvalidHandEditedCap_WarnsAndDoctorShowsIt(t *testing.T) {
	f := relayBoardCLI(t)
	out := f.run(t, "config", "set", "sync.max_queue_size", "5")
	require.Zero(t, out.exitCode, "%s%s", out.stdout, out.stderr)
	cfg := filepath.Join(app.mtixDir, "config.yaml")
	raw, err := os.ReadFile(cfg)
	require.NoError(t, err)
	lines := strings.Split(string(raw), "\n")
	for i, l := range lines {
		if strings.Contains(l, "max_queue_size") {
			lines[i] = l[:strings.LastIndex(l, ":")] + ": lots" // a hand edit
		}
	}
	require.NoError(t, os.WriteFile(cfg, []byte(strings.Join(lines, "\n")), 0o600))

	out = f.run(t, "list")

	require.Contains(t, out.stderr, "sync.max_queue_size")
	require.Contains(t, out.stderr, "queue cap is off")
}
