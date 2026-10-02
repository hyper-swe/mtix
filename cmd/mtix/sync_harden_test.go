// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestSyncHardenCmd_Construction: the command is registered under
// `mtix sync` with its flags (MTIX-95.1).
func TestSyncHardenCmd_Construction(t *testing.T) {
	var harden bool
	for _, c := range newSyncCmd().Commands() {
		if c.Name() == "harden" {
			harden = true
			require.NotEmpty(t, c.Long)
			require.NotNil(t, c.Flags().Lookup("apply"))
			require.NotNil(t, c.Flags().Lookup("keep-role"))
			require.NotNil(t, c.Flags().Lookup("insecure-tls"))
		}
	}
	require.True(t, harden, "sync harden subcommand registered")
}

// TestRunSyncHarden_KeepRole_RefusesUnkeepableNames: --keep-role refuses
// PUBLIC, the data-API roles, predefined roles and invalid names before
// any connection is made (MTIX-95.1).
func TestRunSyncHarden_KeepRole_RefusesUnkeepableNames(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	for _, name := range []string{"public", "PUBLIC", "anon", "authenticated", "pg_read_all_data", "Team-A"} {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runSyncHarden(context.Background(), &stdout, &stderr, nil,
				transport.Options{InsecureTLS: true}, hardenFlags{keepRoles: []string{name}})
			require.Error(t, err)
			require.Equal(t, 1, exitCodeForError(err))
			require.Contains(t, err.Error(), "keep")
			require.NotContains(t, err.Error(), "connect", "refused before connecting")
		})
	}
}

// TestRunSyncHarden_ConfigKeepRoles_InvalidValueRefused: a sync.keep_roles
// value that is not a valid list is refused before any connection.
func TestRunSyncHarden_ConfigKeepRoles_InvalidValueRefused(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	require.NoError(t, writeRawKeepRoles(t, "anon"))
	var stdout, stderr bytes.Buffer
	err := runSyncHarden(context.Background(), &stdout, &stderr, nil,
		transport.Options{InsecureTLS: true}, hardenFlags{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "sync.keep_roles")
}

// TestRunSyncHarden_OutsideProject_Refuses: harden needs an mtix project.
func TestRunSyncHarden_OutsideProject_Refuses(t *testing.T) {
	saveAndResetApp(t)
	var stdout, stderr bytes.Buffer
	err := runSyncHarden(context.Background(), &stdout, &stderr, nil,
		transport.Options{InsecureTLS: true}, hardenFlags{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

// TestKeptRoles_UnionsFlagsAndConfig: the kept set is the union of
// --keep-role and sync.keep_roles, and the config hint names every kept
// role only when the flags add a role the config lacks (MTIX-95.1).
func TestKeptRoles_UnionsFlagsAndConfig(t *testing.T) {
	tests := []struct {
		name     string
		flags    []string
		config   string
		want     []string
		wantHint string
	}{
		{"flags only", []string{"team_b", "team_a"}, "", []string{"team_a", "team_b"},
			"mtix config set sync.keep_roles team_a,team_b"},
		{"config only", nil, "team_a", []string{"team_a"}, ""},
		{"flags already in config", []string{"team_a"}, "team_a,team_b", []string{"team_a", "team_b"}, ""},
		{"flags add to config", []string{"team_c"}, "team_a", []string{"team_a", "team_c"},
			"mtix config set sync.keep_roles team_a,team_c"},
		{"none", nil, "", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, hint, err := keptRoles(tt.flags, tt.config)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantHint, hint)
		})
	}
}

// TestExitCodeForError_HardenPending_Returns2: pending changes or remaining
// exposure exit 2, distinct from an error or a refusal (exit 1).
func TestExitCodeForError_HardenPending_Returns2(t *testing.T) {
	require.Equal(t, 2, exitCodeForError(errHardenPending))
	require.Equal(t, 2, exitCodeForError(fmt.Errorf("wrapped: %w", errHardenPending)))
}

// writeRawKeepRoles writes sync.keep_roles into the test project's config
// file directly, bypassing validation, to model a hand-edited file, then
// reloads the project configuration.
func writeRawKeepRoles(t *testing.T, value string) error {
	t.Helper()
	path := filepath.Join(app.mtixDir, "config.yaml")
	body := "prefix: TEST\n\nsync:\n  keep_roles: " + value + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	cs, err := service.NewConfigService(path)
	if err != nil {
		return err
	}
	app.configSvc = cs
	return nil
}
