// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Detached listener diagnostics must be relay-scoped and read-only.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestDoctorDetachedListener_CLI_RelayScoped(t *testing.T) {
	for _, relay := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "relay"}[relay], func(t *testing.T) {
			dir := ""
			if relay {
				dir = filepath.Join(t.TempDir(), "relay")
			}
			root := doctorCLIBoard(t, dir)
			out := doctorCLIExecute(t, root, "", "doctor", "--json")
			var found bool
			for _, row := range doctorCLIRows(t, out.stdout) {
				if row["id"] == "daemon.detached-listener" {
					found = true
					require.Equal(t, "pass", row["status"], "%v", row)
					require.Contains(t, row["detail"], "no listener")
				}
			}
			require.Equal(t, relay, found)
		})
	}
}

func TestDoctorDetachedListener_States_JSONAndReadOnly(t *testing.T) {
	for _, tc := range []struct {
		name, state, status string
		detail, fix         string // detail is a substring; fix is exact ("" means no fix key)
		change              func(*ctlFake)
	}{
		{"running", "running", "info", "", "", func(f *ctlFake) {}},
		{"stopped", "stopped", "pass", "no listener is running", "", func(f *ctlFake) { f.held = false }},
		{"daemon", "daemon", "info", "held by a daemon, not a listener", "mtix daemon status --listener",
			func(f *ctlFake) { f.proc.Args = []string{"mtix", "daemon"} }},
		{"unverified", "unverified", "warn", "the lock holder is not verified", "mtix daemon status --listener",
			func(f *ctlFake) { f.proc.Args = nil }},
		{"lock error", "", "warn", "", "mtix daemon status --listener", func(f *ctlFake) { f.lockErr = errors.New("lock read failed") }},
		{"lease error", "", "warn", "", "mtix daemon status --listener", func(f *ctlFake) { f.leaseErr = errors.New("lease read failed") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCtlFake()
			f.waiter()
			tc.change(f)
			svc := service.NewDoctorService(service.DoctorOptions{})
			require.NoError(t, registerDetachedListenerCheck(svc, true, func() (*listenerCtl, error) { return f.ctl(), nil }))
			for _, fix := range []bool{false, true} {
				var out bytes.Buffer
				require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: true, Fix: fix}))
				var rows []map[string]any
				require.NoError(t, json.Unmarshal(out.Bytes(), &rows))
				require.Len(t, rows, 1)
				row := rows[0]
				require.Equal(t, "daemon.detached-listener", row["id"])
				require.Equal(t, tc.status, row["status"])
				assertDetachedListenerJSON(t, row, tc.state)
				if tc.detail != "" {
					require.Contains(t, row["detail"], tc.detail)
				}
				if tc.state != "running" {
					if tc.fix == "" {
						require.Empty(t, row["fix"])
					} else {
						require.Equal(t, tc.fix, row["fix"])
					}
				}
			}
			require.Empty(t, f.signals)
			require.Zero(t, f.stopSet)
			require.Zero(t, f.stopClr)
		})
	}
}

func TestDoctorDetachedListener_RegistrationAndFactoryErrors(t *testing.T) {
	for _, relay := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[relay], func(t *testing.T) {
			calls := 0
			svc := service.NewDoctorService(service.DoctorOptions{})
			factory := func() (*listenerCtl, error) { calls++; return nil, errors.New("source unavailable") }
			require.NoError(t, registerDetachedListenerCheck(svc, relay, factory))
			rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
			require.NoError(t, err)
			if !relay {
				require.Empty(t, rows)
				require.Zero(t, calls)
				return
			}
			require.Len(t, rows, 1)
			require.Equal(t, service.DoctorWarn, rows[0].Status)
			require.Contains(t, rows[0].Detail, "source unavailable")
			require.Equal(t, "mtix daemon status --listener", rows[0].Fix)
			require.Error(t, registerDetachedListenerCheck(svc, true, factory))
		})
	}
}

func assertDetachedListenerJSON(t *testing.T, row map[string]any, state string) {
	t.Helper()
	if state != "" {
		require.Len(t, row, 5)
		data := row["data"].(map[string]any)
		require.Equal(t, state, data["state"])
		require.Len(t, data["waiters"], 1)
	} else {
		require.Len(t, row, 4)
		require.Contains(t, row["detail"], "read failed")
	}
	if state == "running" {
		require.Equal(t, "mtix daemon stop --listener && mtix daemon status --listener", row["fix"])
		require.Contains(t, row["detail"], "exit 10")
		require.Contains(t, row["detail"], "does not release")
		require.Equal(t, float64(42), row["data"].(map[string]any)["pid"])
	}
}

// TestRegisterDaemonChecks_DetachedListenerRegistrationFails pins that
// registerDaemonChecks wraps and returns a failed detached-listener registration.
func TestRegisterDaemonChecks_DetachedListenerRegistrationFails(t *testing.T) {
	initTestApp(t)
	configPath := filepath.Join(app.mtixDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath,
		[]byte("prefix: TEST\nsync:\n  relay.dir: "+strconv.Quote(filepath.Join(t.TempDir(), "relay"))+"\n"), 0o600))
	cfg, err := service.NewConfigService(configPath)
	require.NoError(t, err)
	app.configSvc = cfg
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, svc.Register(service.DoctorCheck{ID: "daemon.detached-listener",
		Run: func(context.Context) service.DoctorResult { return service.DoctorResult{Status: service.DoctorPass} }}))
	err = registerDaemonChecks(svc)
	require.Error(t, err)
	require.Contains(t, err.Error(), "register detached listener diagnostics")
}
