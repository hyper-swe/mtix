// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

type daemonFailureCase struct {
	name, code, class, fix string
	hook                   bool
}

func TestRunDaemon_PullFailure_RecordsRealOutcome(t *testing.T) {
	cases := []daemonFailureCase{
		{name: "authentication", code: "28P01", class: "auth", fix: "repair the hub credential in MTIX_SYNC_DSN, MTIX_SYNC_PASSWORD or .mtix/secrets, then run: mtix sync pull"},
		{name: "authentication class", code: "28000", class: "auth", fix: "repair the hub credential in MTIX_SYNC_DSN, MTIX_SYNC_PASSWORD or .mtix/secrets, then run: mtix sync pull"},
		{name: "startup unavailable", code: "57P03", class: "unreachable", fix: "restore network reachability to the configured hub, then run: mtix sync pull"},
		{name: "authorization", code: "42501", class: "refused", fix: "ask the hub operator to restore pull permissions, then run: mtix sync pull"},
		{name: "unreachable", code: "08006", class: "unreachable", fix: "restore network reachability to the configured hub, then run: mtix sync pull"},
		{name: "timeout", code: "57014", class: "timeout", fix: "check hub availability and query load, then run: mtix sync pull"},
		{name: "other", code: "XX000", class: "pull_failed", fix: "run: mtix sync pull; then run: mtix sync doctor"},
		{name: "hook warning", code: "08006", class: "unreachable", hook: true, fix: "restore network reachability to the configured hub, then run: mtix sync pull"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { checkDaemonPullFailure(t, tc) })
	}
}

func checkDaemonPullFailure(t *testing.T, tc daemonFailureCase) {
	t.Helper()
	src := daemonOutcomeFixture(t)
	dsn, calls := daemonErrorHub(t, tc.code)
	t.Setenv(transport.EnvDSN, dsn)
	if tc.hook {
		t.Setenv("MTIX_SYNC_HOOK", "1")
	} else {
		t.Setenv("MTIX_SYNC_HOOK", "")
	}
	stdout, stderr := runOutcomeDaemonOnce(t)
	require.Positive(t, calls.Load(), "the real daemon must reach the wire hub")
	rec, present, err := src.state.ReadHubContact(src.key())
	require.NoError(t, err)
	require.True(t, present)
	require.False(t, rec.OK, "a failed real pull must never be recorded as success")
	raw, err := json.Marshal(rec)
	require.NoError(t, err)
	require.Contains(t, string(raw), fmt.Sprintf(`"error_class":"%s"`, tc.class))
	row := hubContactRow(src, true, src.now())
	text, err := json.Marshal(row)
	require.NoError(t, err)
	require.False(t, row.Pass)
	require.Contains(t, row.Detail, tc.class)
	require.Contains(t, row.Fix, tc.fix)
	require.Contains(t, string(text), tc.class)
	require.Contains(t, string(text), tc.fix)
	require.NotContains(t, stdout+stderr+string(raw)+string(text), "daemon-outcome-secret")
	if tc.hook {
		require.Contains(t, stderr, "WARN:")
		require.NotContains(t, stderr, "pull error (will retry)")
	}
}

func runOutcomeDaemonOnce(t *testing.T) (string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	require.NoError(t, runDaemon(ctx, &stdout, &stderr, nil, transport.Options{InsecureTLS: true}, 5,
		daemonPassControl{observed: func(bool) { cancel() }}))
	return stdout.String(), stderr.String()
}

func daemonOutcomeFixture(t *testing.T) *hubContactSource {
	t.Helper()
	src := hubSourceFixture(t, "prefix: X\n")
	canonical, err := canonicalBoardDir(src.mtixDir)
	require.NoError(t, err)
	src.mtixDir = canonical
	store := newDaemonTestStore(t, src.mtixDir)
	cfg, err := service.NewConfigService(src.configPath())
	require.NoError(t, err)
	saved := app
	app = appContext{mtixDir: src.mtixDir, store: store, logger: slog.Default(), configSvc: cfg,
		hooksDisp: service.NewHooksDispatcher(store, src.mtixDir, slog.Default())}
	t.Cleanup(func() { app = saved })
	return src
}
