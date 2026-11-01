// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Legacy 0.5.x per-project service detection, doctor flag and unit removal (ADR-007 D11).
package main

import (
	"context"
	"errors"
	"fmt"
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
	probe := legacyServiceProbe(runtime.GOOS, func() (string, error) { return t.TempDir(), nil })
	liveProbe, err := probe(t.Context(), dir)
	require.NoError(t, err)
	require.False(t, liveProbe)
	body, readErr := os.ReadFile(filepath.Join(dir, daemonPIDFilename))
	require.NoError(t, readErr)
	require.NotEmpty(t, body, "the probe never deletes the marker")
}

func TestLegacyServiceProbe_DaemonStartedSecond_DetectedThenReleased(t *testing.T) {
	root := t.TempDir()
	mtixDir := filepath.Join(root, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o700))
	probe := legacyServiceProbe(runtime.GOOS, func() (string, error) { return t.TempDir(), nil })
	live, err := probe(t.Context(), mtixDir)
	require.NoError(t, err)
	require.False(t, live, "no daemon yet")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLegacyDaemonHelperProcess$", "--", "-C", root, "daemon") // #nosec G204 -- the test binary re-executes itself
	cmd.Dir = root                                                                                           // a daemon's working directory is its project
	cmd.Env = append(os.Environ(), "MTIX_LEGACY_HELPER=1")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	require.NoError(t, writeDaemonPID(mtixDir, cmd.Process.Pid))
	live, err = probe(t.Context(), mtixDir)
	require.NoError(t, err)
	require.True(t, live, "a daemon that starts second is seen on the next pass")
	require.NoError(t, cmd.Process.Kill())
	_ = cmd.Wait()
	live, err = probe(t.Context(), mtixDir)
	require.NoError(t, err)
	require.False(t, live, "an exited daemon no longer owns duty")
}

func TestLegacyUnitInstalled_UnitFilePresence(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			installed, _, err := legacyUnitInstalled(goos, root, home)
			require.NoError(t, err)
			require.False(t, installed)
			spec, err := buildServiceSpec(goos, "mtix", root, home, daemonDispatchDefaultIntervalSec)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(spec.Path), 0o700))
			require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
			installed, _, err = legacyUnitInstalled(goos, root, home)
			require.NoError(t, err)
			require.True(t, installed)
		})
	}
	installed, _, err := legacyUnitInstalled("windows", t.TempDir(), t.TempDir())
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
}

// exitErr is a service-manager failure carrying a process exit code.
type exitErr struct{ code int }

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e exitErr) ExitCode() int { return e.code }

// runWith fakes the service runner: unregister fails, and the status command returns statusErr
// (nil means the label is still loaded).
func runWith(spec *serviceSpec, statusErr error, statusRuns *int) func(context.Context, io.Writer, [][]string, bool) error {
	return func(_ context.Context, _ io.Writer, cmds [][]string, _ bool) error {
		if fmt.Sprint(cmds) == fmt.Sprint(spec.Status) {
			*statusRuns++
			return statusErr
		}
		return errors.New("service manager failed")
	}
}

func TestUninstallLegacyService_StatusDoesNotProveNotLoaded_KeepsErrorAndUnit(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		probe, err := buildServiceSpec(goos, "mtix", t.TempDir(), t.TempDir(), daemonDispatchDefaultIntervalSec)
		require.NoError(t, err)
		for name, statusErr := range map[string]error{
			"still-loaded":                nil,
			"manager-unreachable":         errors.New("Failed to connect to bus"),
			"exit-code-not-the-not-found": exitErr{code: 3},
			"exit-code-one":               exitErr{code: 1},
			"binary-missing":              &os.PathError{Op: "exec", Path: "systemctl", Err: os.ErrNotExist},
		} {
			t.Run(goos+"/"+name, func(t *testing.T) {
				home, root := t.TempDir(), t.TempDir()
				installUnit(t, goos, root, home)
				spec, err := buildServiceSpec(goos, "mtix", root, home, daemonDispatchDefaultIntervalSec)
				require.NoError(t, err)
				var statusRuns int
				require.Error(t, uninstallLegacyService(t.Context(), io.Discard, spec, runWith(spec, statusErr, &statusRuns)))
				require.Equal(t, 1, statusRuns)
				require.FileExists(t, spec.Path, "a unit whose service may still be registered is not removed")
			})
		}
		require.NotZero(t, probe.NotLoadedExit, goos+" can prove a label is not loaded")
	}
}

func TestUninstallLegacyService_AlreadyUnloaded_RerunConverges(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			installUnit(t, goos, root, home)
			spec, err := buildServiceSpec(goos, "mtix", root, home, daemonDispatchDefaultIntervalSec)
			require.NoError(t, err)
			var statusRuns int
			notLoaded := exitErr{code: spec.NotLoadedExit}
			require.NoError(t, uninstallLegacyService(t.Context(), io.Discard, spec, runWith(spec, notLoaded, &statusRuns)),
				"the label is gone after the first run: the re-run still completes")
			require.Equal(t, 1, statusRuns)
			_, statErr := os.Stat(spec.Path)
			require.ErrorIs(t, statErr, os.ErrNotExist)
		})
	}
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
			daemonPID := 0
			if tc.daemon {
				daemonPID = startLegacyDaemonHelper(t, filepath.Dir(app.mtixDir))
				require.NoError(t, writeDaemonPID(app.mtixDir, daemonPID))
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
			require.Equal(t, legacyServiceFix, found.Fix)
			require.Equal(t, "mtix daemon uninstall", found.Fix, "the fix is a command this build ships")
			require.NotContains(t, found.Fix, "mtix setup")
			require.Equal(t, tc.daemon, found.Data["daemon_live"])
			if tc.daemon {
				require.Equal(t, daemonPID, found.Data["daemon_pid"], "doctor reports the owning pid")
			}
			require.Equal(t, tc.unit, found.Data["unit_installed"])
		})
	}
}

func TestHostDispatcher_LegacyService_DoctorWiredForEnrolledAndUnenrolledBoards(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		t.Run(map[bool]string{false: "unenrolled", true: "enrolled"}[enrolled], func(t *testing.T) {
			initRegistrationApp(t)
			require.NoError(t, runAgentRegister("legacy-worker", sessionOptions{}))
			require.NoError(t, writeDaemonPID(app.mtixDir, startLegacyDaemonHelper(t, filepath.Dir(app.mtixDir))))
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
			require.NotContains(t, found.Fix, "mtix setup")
		})
	}
}

func TestLegacyServiceProbe_InstalledUnitOwnsDutyWhileDaemonDown(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			home, root := t.TempDir(), t.TempDir()
			mtixDir := filepath.Join(root, ".mtix")
			require.NoError(t, os.MkdirAll(mtixDir, 0o700))
			probe := legacyServiceProbe(goos, func() (string, error) { return home, nil })
			live, err := probe(t.Context(), mtixDir)
			require.NoError(t, err)
			require.False(t, live, "no daemon and no unit: the host owns duty")
			spec, err := buildServiceSpec(goos, "mtix", root, home, daemonDispatchDefaultIntervalSec)
			require.NoError(t, err)
			require.NoError(t, os.MkdirAll(filepath.Dir(spec.Path), 0o700))
			require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
			require.NoError(t, writeDaemonPID(mtixDir, 999999999)) // crash-restart gap: marker names a dead process
			live, err = probe(t.Context(), mtixDir)
			require.NoError(t, err)
			require.True(t, live, "an installed unit keeps pull and hook duty while its daemon is down")
		})
	}
}

func TestLegacyServiceProbe_HomeUnresolvable_FailsClosed(t *testing.T) {
	root := t.TempDir()
	probe := legacyServiceProbe("linux", func() (string, error) { return "", errors.New("no home") })
	_, err := probe(t.Context(), filepath.Join(root, ".mtix"))
	require.Error(t, err, "the unit check cannot be skipped silently")
}

func TestLegacyServiceProbe_LiveDaemon_NeedsNoUnit(t *testing.T) {
	root := t.TempDir()
	mtixDir := filepath.Join(root, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o700))
	require.NoError(t, writeDaemonPID(mtixDir, startLegacyDaemonHelper(t, root)))
	probe := legacyServiceProbe("linux", func() (string, error) { return "", errors.New("must not be asked") })
	live, err := probe(t.Context(), mtixDir)
	require.NoError(t, err)
	require.True(t, live)
}

func TestDaemonPIDFile_OtherMtixProcessOnReusedPID_NotLive(t *testing.T) {
	root := t.TempDir()
	mtixDir := filepath.Join(root, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o700))
	for name, args := range map[string][]string{
		"another-project-daemon": {"-C", t.TempDir(), "daemon"},
		"mtix-mcp-server":        {"-C", root, "mcp"},
		"mtix-without-daemon":    {},
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, writeDaemonPID(mtixDir, startLegacyDaemonHelper(t, "", args...)))
			live, _, err := daemonPIDFileLive(mtixDir)
			require.NoError(t, err)
			require.False(t, live, "a reused pid held by another mtix process is stale")
		})
	}
}

// startLegacyDaemonHelper starts a real process that looks like a 0.5.x daemon in the process
// table: this test binary (named mtix.test) re-executed with the argv mtix -C root daemon.
// An empty root with explicit args starts the same binary with exactly those arguments.
func startLegacyDaemonHelper(t *testing.T, root string, args ...string) int {
	t.Helper()
	argv := []string{"-test.run=^TestLegacyDaemonHelperProcess$", "--"}
	if root != "" {
		argv = append(argv, "-C", root, "daemon")
	}
	argv = append(argv, args...)
	cmd := exec.Command(os.Args[0], argv...) // #nosec G204 -- the test binary re-executes itself
	cmd.Dir = root                           // a daemon's working directory is its project; "" keeps the test's own
	cmd.Env = append(os.Environ(), "MTIX_LEGACY_HELPER=1")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return cmd.Process.Pid
}

// TestLegacyDaemonHelperProcess is the body of the helper process; it does nothing in a normal run.
func TestLegacyDaemonHelperProcess(t *testing.T) {
	if os.Getenv("MTIX_LEGACY_HELPER") != "1" {
		return
	}
	time.Sleep(time.Minute)
}
