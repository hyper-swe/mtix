// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestPendingExports_NativeCLIStatusAndDoctorDoNotDrain(t *testing.T) {
	for _, mode := range []string{"status", "doctor"} {
		t.Run(mode, func(t *testing.T) { pendingDiagnosticsProcess(t, mode) })
	}
}

func pendingDiagnosticsProcess(t *testing.T, mode string) {
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("prefix: TEST\n"), 0644))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	svc := service.NewSyncService(store, logger, func() time.Time { return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC) })
	require.NoError(t, svc.ForceExport(context.Background(), dir))
	require.NoError(t, svc.RequestExport(dir))
	state, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	files := pendingDiagnosticFiles(t, dir)
	meta := pendingDiagnosticMeta(t, store)
	raw := runPendingDiagnosticChild(t, dir, mode)
	var decoded map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &decoded), string(raw))
	if mode == "status" {
		var status service.PendingExportStatus
		require.NoError(t, json.Unmarshal(decoded["pending_exports"], &status))
		require.Equal(t, 1, status.Count)
		require.Equal(t, state.Oldest, status.OldestAt)
	} else {
		check := doctorCheckFromJSON(t, raw, "pending exports")
		require.True(t, check.Warn)
		require.Equal(t, "mtix list", check.Fix)
	}
	after, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	require.Equal(t, state, after, "native preRun must not drain or retire diagnostic work")
	require.Equal(t, files, pendingDiagnosticFiles(t, dir))
	require.Equal(t, meta, pendingDiagnosticMeta(t, store))
}

func runPendingDiagnosticChild(t *testing.T, dir, mode string) []byte {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestPendingExports_CLIProcessHelper$", "--", mode)
	cmd.Dir = filepath.Dir(dir)
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if !strings.HasPrefix(key, "MTIX_") && !strings.HasPrefix(key, "PG") && key != "XDG_CONFIG_HOME" {
			cmd.Env = append(cmd.Env, value)
		}
	}
	operator := t.TempDir()
	cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+operator)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err = cmd.Run() // Owns and joins the exact process and both output-copy workers.
	if mode == "doctor" {
		var exit *exec.ExitError
		require.True(t, errors.As(err, &exit), "native no-DSN doctor must exit2: %s", errOut.String())
		require.Equal(t, 2, exit.ExitCode())
	} else {
		require.NoError(t, err, errOut.String())
	}
	return out.Bytes()
}

func pendingDiagnosticFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	for _, name := range []string{"tasks.json", "data/sync.sha256", "data/sync-db.sha256"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		files[name] = string(content)
	}
	return files
}

func pendingDiagnosticMeta(t *testing.T, store *sqlite.Store) map[string]string {
	t.Helper()
	rows, err := store.Query(context.Background(), "SELECT key,value FROM meta ORDER BY key")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	meta := make(map[string]string)
	for rows.Next() {
		var key, value string
		require.NoError(t, rows.Scan(&key, &value))
		meta[key] = value
	}
	require.NoError(t, rows.Err())
	return meta
}

func TestPendingExports_CLIProcessHelper(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	require.Contains(t, []string{"status", "doctor"}, mode)
	root := newRootCmd()
	root.SetArgs([]string{"sync", mode, "--json"})
	require.NoError(t, root.Execute())
	if app.store != nil {
		require.NoError(t, app.store.Close())
	}
	os.Exit(0) // Native JSON stdout must not include testing's PASS footer.
}
