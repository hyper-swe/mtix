// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

func TestHubContactRow_UnreadableHistoricalRecord_RespectsDisabledPolicy(t *testing.T) {
	for _, hub := range []bool{false, true} {
		t.Run(map[bool]string{false: "no hub", true: "manual"}[hub], func(t *testing.T) {
			src := daemonOutcomeFixture(t)
			_, err := app.configSvc.Set("sync.hub_contact", "manual")
			require.NoError(t, err)
			require.NoError(t, src.state.WriteJSON("hubcontact-"+src.key(), map[string]any{"at": "unreadable-record-secret"}))
			row := hubContactRow(src, hub, src.now())
			require.True(t, row.Pass)
			require.Equal(t, hub, row.Warn)
			require.Contains(t, row.Detail, "unreadable")
			require.NotContains(t, row.Detail+row.Fix, "unreadable-record-secret")
			if hub {
				require.Contains(t, row.Fix, "mtix sync pull")
			} else {
				require.NotContains(t, row.Fix, "mtix sync pull")
			}
		})
	}
}

func TestDoctorCLI_UnreadableContact_PrintedPullRepairsRecord(t *testing.T) {
	_ = openCmdHub(t)
	dsn := os.Getenv(transport.EnvDSN)
	for _, mode := range []string{"manual", "activity"} {
		t.Run(mode, func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			src := seedPublicContactFailure(t, root)
			cfg, err := service.NewConfigService(src.configPath())
			require.NoError(t, err)
			_, err = cfg.Set("sync.hub_contact", mode)
			require.NoError(t, err)
			require.NoError(t, src.state.WriteJSON("hubcontact-"+src.key(), map[string]any{"at": "unreadable-repair-secret"}))
			before := publicContactRow(t, doctorCLIExecute(t, root, dsn, "doctor", "--json").stdout)
			if mode == "manual" {
				require.Equal(t, "warn", before.Status)
			} else {
				require.Equal(t, "fail", before.Status)
			}
			i := strings.LastIndex(before.Fix, "mtix sync pull")
			require.NotEqual(t, -1, i)
			command := strings.Fields(before.Fix[i:])
			pull := doctorCLIExecute(t, root, dsn, append(command[1:], "--insecure-tls")...)
			require.Zero(t, pull.exit, pull.stderr)
			after := publicContactRow(t, doctorCLIExecute(t, root, dsn, "doctor", "--json").stdout)
			require.NotEqual(t, "fail", after.Status)
			require.NotContains(t, after.Detail, "unreadable")
			require.Contains(t, after.Detail, "last hub contact succeeded")
		})
	}
}

func TestRunOneDaemonPull_PreDialSecretsFailure_NamesHistoricalHubContact(t *testing.T) {
	src := daemonOutcomeFixture(t)
	prior := oplocal.HubContact{At: src.now(), Route: "poll", OK: true}
	require.NoError(t, src.state.RecordHubContact(src.key(), prior))
	dsn, calls := daemonErrorHub(t, "28P01")
	t.Setenv(transport.EnvDSN, "")
	path := filepath.Join(app.mtixDir, transport.SecretsFilename)
	require.NoError(t, os.WriteFile(path, []byte(dsn), 0o600))
	hub, err := daemonPreflight(nil)
	require.NoError(t, err)
	require.True(t, hub)
	require.NoError(t, os.Chmod(path, 0o644)) //nolint:gosec // intentional permission fault after preflight
	var stderr bytes.Buffer
	reports := 0
	outcome := runOneDaemonPull(t.Context(), &stderr, nil, transport.Options{InsecureTLS: true}, daemonPullControl{onOutcome: func(daemonPullOutcome) { reports++ }})
	require.False(t, outcome.attempted)
	require.False(t, outcome.ok)
	require.Zero(t, reports)
	require.Zero(t, calls.Load())
	require.Contains(t, stderr.String(), "pull error")
	rec, present, err := src.state.ReadHubContact(src.key())
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, prior, rec)
	row := hubContactRow(src, true, src.now())
	require.Contains(t, row.Detail, "last hub contact succeeded")
	require.NotContains(t, row.Detail, "last pull succeeded")
}

func TestSyncDoctorHelp_ContactOutcomeIncludesExplicitPull(t *testing.T) {
	cmd := newSyncDoctorCmd()
	cmd.SetOut(io.Discard)
	require.Contains(t, cmd.Long, "last pull outcome")
	require.NotContains(t, cmd.Long, "last scheduled daemon pull")
}
