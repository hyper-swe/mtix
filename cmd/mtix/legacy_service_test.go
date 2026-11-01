// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Legacy 0.5.x per-project service detection, doctor flag and unit removal (ADR-007 D11).
package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestHostDispatcher_StalePIDReused_NotTreatedLive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a unix sleep process as the unrelated pid holder")
	}
	other := exec.Command("sleep", "30") // #nosec G204 -- fixed argv for a test-owned process
	require.NoError(t, other.Start())
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	dir := t.TempDir()
	require.NoError(t, writeDaemonPID(dir, other.Process.Pid))
	live, _, err := daemonPIDFileLive(dir)
	require.NoError(t, err)
	require.False(t, live, "a reused pid belonging to a non-mtix process is not a live legacy service")
	liveProbe, err := legacyServiceLive(t.Context(), dir)
	require.NoError(t, err)
	require.False(t, liveProbe)
	body, readErr := os.ReadFile(filepath.Join(dir, daemonPIDFilename))
	require.NoError(t, readErr)
	require.NotEmpty(t, body, "the probe never deletes the marker")
}

func TestLegacyServiceLive_RealOwnerStartedSecond_DetectedThenReleased(t *testing.T) {
	dir := t.TempDir()
	live, err := legacyServiceLive(t.Context(), dir)
	require.NoError(t, err)
	require.False(t, live, "no daemon yet")
	owner, err := acquireDaemonOwnership(dir)
	require.NoError(t, err)
	live, err = legacyServiceLive(t.Context(), dir)
	require.NoError(t, err)
	require.True(t, live, "a daemon that starts second is seen on the next pass")
	require.NoError(t, owner.release())
	live, err = legacyServiceLive(t.Context(), dir)
	require.NoError(t, err)
	require.False(t, live, "a released daemon no longer owns duty")
}

func TestPidIsMtixProcess_Table(t *testing.T) {
	require.True(t, pidIsMtixProcess(os.Getpid()), "the test binary is an mtix executable")
	require.False(t, pidIsMtixProcess(999999999), "an absent pid is not a daemon")
}

func TestLegacyUnitInstalled_UnitFilePresence(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			installed, err := legacyUnitInstalled(goos, root, home)
			require.NoError(t, err)
			require.False(t, installed)
			spec, err := buildServiceSpec(goos, "mtix", root, home, daemonDispatchDefaultIntervalSec)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(spec.Path), 0o700))
			require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
			installed, err = legacyUnitInstalled(goos, root, home)
			require.NoError(t, err)
			require.True(t, installed)
		})
	}
	installed, err := legacyUnitInstalled("windows", t.TempDir(), t.TempDir())
	require.NoError(t, err)
	require.False(t, installed, "Task Scheduler has no unit file; doctor relies on the PID file")
}

func TestUninstallLegacyService_UnregistersThenRemovesUnit(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	spec, err := buildServiceSpec("linux", "mtix", root, home, daemonDispatchDefaultIntervalSec)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(spec.Path), 0o700))
	require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
	var ran [][]string
	run := func(_ context.Context, _ io.Writer, cmds [][]string, lenient bool) error {
		require.True(t, lenient)
		ran = append(ran, cmds...)
		return nil
	}
	require.NoError(t, uninstallLegacyService(t.Context(), io.Discard, spec, run))
	require.Equal(t, spec.Unregister, ran)
	_, statErr := os.Stat(spec.Path)
	require.ErrorIs(t, statErr, os.ErrNotExist)
	require.NoError(t, uninstallLegacyService(t.Context(), io.Discard, spec, run), "idempotent when the unit is already gone")
	failing := func(context.Context, io.Writer, [][]string, bool) error { return errors.New("systemctl failed") }
	require.Error(t, uninstallLegacyService(t.Context(), io.Discard, spec, failing))
}

func TestHostDispatcher_LegacyService_DoctorFlagsWithSetupFix(t *testing.T) {
	for _, tc := range []struct {
		name         string
		daemon, unit bool
		want         bool
	}{{"nothing", false, false, false}, {"live-daemon", true, false, true}, {"unit-only", false, true, true}} {
		t.Run(tc.name, func(t *testing.T) {
			initRegistrationApp(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			goos := runtime.GOOS
			if goos != "darwin" {
				goos = "linux"
			}
			if tc.daemon {
				require.NoError(t, writeDaemonPID(app.mtixDir, os.Getpid()))
			}
			if tc.unit {
				spec, err := buildServiceSpec(goos, "mtix", filepath.Dir(app.mtixDir), home, daemonDispatchDefaultIntervalSec)
				require.NoError(t, err)
				require.NoError(t, os.MkdirAll(filepath.Dir(spec.Path), 0o700))
				require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
			}
			svc := service.NewDoctorService(service.DoctorOptions{})
			require.NoError(t, registerLegacyServiceDoctor(svc, app.mtixDir, goos, home))
			rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
			require.NoError(t, err)
			var found *service.DoctorResult
			for i := range rows {
				if rows[i].ID == "dispatcher.legacy-service" {
					found = &rows[i]
				}
			}
			if !tc.want {
				require.Nil(t, found)
				return
			}
			require.NotNil(t, found)
			require.Equal(t, service.DoctorWarn, found.Status)
			require.Equal(t, "mtix setup", found.Fix)
			require.Equal(t, tc.daemon, found.Data["daemon_live"])
			require.Equal(t, tc.unit, found.Data["unit_installed"])
		})
	}
}

func TestHostDispatcher_LegacyService_DoctorWiredForEnrolledAndUnenrolledBoards(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		t.Run(map[bool]string{false: "unenrolled", true: "enrolled"}[enrolled], func(t *testing.T) {
			initRegistrationApp(t)
			require.NoError(t, runAgentRegister("legacy-worker", sessionOptions{}))
			require.NoError(t, writeDaemonPID(app.mtixDir, os.Getpid()))
			state, err := operatorState()
			require.NoError(t, err)
			if enrolled {
				root, evalErr := filepath.EvalSymlinks(filepath.Dir(app.mtixDir))
				require.NoError(t, evalErr)
				instance, idErr := app.store.StoreInstanceID(t.Context())
				require.NoError(t, idErr)
				hub := oplocal.HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "observed"}
				entry := &oplocal.EnrolledBoard{Path: root, BoardID: instance, StoreInstanceID: instance, Hub: hub}
				require.NoError(t, state.SetEnrollment(t.Context(), entry, oplocal.EnrollmentAudit{Path: root, Action: "enroll", Mode: "yes", Source: "test", At: time.Now().UTC()}))
			}
			svc := service.NewDoctorService(service.DoctorOptions{})
			require.NoError(t, registerHostDispatcherDoctor(svc))
			rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
			require.NoError(t, err)
			var found *service.DoctorResult
			for i := range rows {
				if rows[i].ID == "dispatcher.legacy-service" {
					found = &rows[i]
				}
			}
			require.NotNil(t, found)
			require.Equal(t, service.DoctorWarn, found.Status)
			require.Equal(t, "mtix setup", found.Fix)
		})
	}
}
