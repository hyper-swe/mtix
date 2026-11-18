// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/service"
)

func runDaemonServiceDoctor(t *testing.T, spec *serviceSpec, live func() (bool, int, error), fm *fakeManager) *service.DoctorResult {
	t.Helper()
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, registerDaemonServiceStateCheck(svc, spec, live, fm.run))
	rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
	require.NoError(t, err)
	for i := range rows {
		if rows[i].ID == daemonServiceStoppedLiveID {
			return &rows[i]
		}
	}
	return nil
}

func installedSpec(t *testing.T) *serviceSpec {
	t.Helper()
	spec := specFor(t, "darwin")
	spec.Path = filepath.Join(t.TempDir(), "unit.plist")
	require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
	return spec
}

func TestDaemonServiceStateDoctor_StoppedButLive_WarnsWithKillFix(t *testing.T) {
	fm := &fakeManager{status: func() (string, error) { return "", exitErr{code: 113} }}
	row := runDaemonServiceDoctor(t, installedSpec(t), func() (bool, int, error) { return true, 4242, nil }, fm)
	require.NotNil(t, row)
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Equal(t, daemonServiceStoppedLiveFix, row.Fix)
	assert.NotContains(t, strings.ToLower(row.Fix+row.Detail), "kill", "agents are never told to kill a pid")
	assert.Contains(t, row.Fix, "mtix daemon stop")
	assert.Contains(t, row.Fix, "mtix daemon status")
	assert.Contains(t, row.Detail, "mtix daemon uninstall")
	assert.Equal(t, 4242, row.Data["daemon_pid"])
	assert.Equal(t, false, row.Data["service_running"])
}

func TestDaemonServiceStateDoctor_ConsistentStates_Pass(t *testing.T) {
	running := &fakeManager{status: func() (string, error) { return "state = running", nil }}
	stopped := &fakeManager{status: func() (string, error) { return "", exitErr{code: 113} }}
	for name, tc := range map[string]struct {
		fm   *fakeManager
		live bool
	}{"running and live": {running, true}, "stopped and not live": {stopped, false}} {
		t.Run(name, func(t *testing.T) {
			row := runDaemonServiceDoctor(t, installedSpec(t), func() (bool, int, error) { return tc.live, 7, nil }, tc.fm)
			require.NotNil(t, row)
			assert.Equal(t, service.DoctorPass, row.Status)
		})
	}
}

func TestDaemonServiceStateDoctor_NoUnit_NoRow(t *testing.T) {
	spec := specFor(t, "darwin")
	spec.Path = filepath.Join(t.TempDir(), "absent.plist")
	assert.Nil(t, runDaemonServiceDoctor(t, spec, func() (bool, int, error) { return true, 1, nil }, &fakeManager{}))
	assert.Nil(t, runDaemonServiceDoctor(t, specFor(t, "windows"), func() (bool, int, error) { return true, 1, nil }, &fakeManager{}), "no unit file on windows")
}

func TestDaemonServiceStateDoctor_ErrorsWarnWithoutAGuess(t *testing.T) {
	missing := &fakeManager{status: func() (string, error) { return "", &os.PathError{Op: "exec", Path: "launchctl", Err: os.ErrNotExist} }}
	row := runDaemonServiceDoctor(t, installedSpec(t), func() (bool, int, error) { return true, 1, nil }, missing)
	require.NotNil(t, row)
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Equal(t, "mtix daemon status", row.Fix)

	stopped := &fakeManager{status: func() (string, error) { return "", exitErr{code: 113} }}
	row = runDaemonServiceDoctor(t, installedSpec(t), func() (bool, int, error) { return false, 0, errors.New("read pid file") }, stopped)
	require.NotNil(t, row)
	assert.Equal(t, service.DoctorWarn, row.Status)
}

// The doctor row must be registered by the real wiring, not only by the helper (MTIX-275 review S2-3).
func TestRegisterDaemonChecks_InstalledUnit_RegistersStoppedLiveRow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	prev := app.mtixDir
	t.Cleanup(func() { app.mtixDir = prev })
	app.mtixDir = filepath.Join(t.TempDir(), ".mtix")
	spec, err := currentServiceSpec(daemonDispatchDefaultIntervalSec)
	require.NoError(t, err)
	if spec.Path == "" {
		t.Skip("platform has no unit file")
	}
	has := func() bool {
		svc := service.NewDoctorService(service.DoctorOptions{})
		require.NoError(t, registerDaemonChecks(svc))
		// A second registration of the same id is refused only when the row is already wired.
		dup := svc.Register(service.DoctorCheck{ID: daemonServiceStoppedLiveID, Run: func(context.Context) service.DoctorResult { return service.DoctorResult{} }})
		return errors.Is(dup, model.ErrAlreadyExists)
	}
	assert.False(t, has(), "no unit file, no row")
	require.NoError(t, os.MkdirAll(filepath.Dir(spec.Path), 0o755)) //nolint:gosec // test dir
	require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
	assert.True(t, has(), "an installed unit registers the row through registerDaemonChecks")
}

// The doctor row decides "live" fail-closed: when the process identity cannot be proven, it
// reports unknown (no advice), never live (MTIX-275 review).
func TestDaemonPIDFileLiveStrict_UnprovenIdentity_ReportsErrorNotLive(t *testing.T) {
	mtixDir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755)) //nolint:gosec // test dir
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, daemonPIDFilename), []byte(strconv.Itoa(os.Getpid())), 0o600))
	root := filepath.Dir(mtixDir)
	tests := map[string]func(int) (procinfo.Proc, error){
		"lookup fails":    func(int) (procinfo.Proc, error) { return procinfo.Proc{}, errors.New("denied") },
		"unknown name":    func(int) (procinfo.Proc, error) { return procinfo.Proc{}, nil },
		"unreadable args": func(int) (procinfo.Proc, error) { return procinfo.Proc{Name: "mtix"}, nil },
		"empty cwd": func(int) (procinfo.Proc, error) {
			return procinfo.Proc{Name: "mtix", Args: []string{"mtix", "daemon"}}, nil
		},
	}
	for name, lookup := range tests {
		t.Run(name, func(t *testing.T) {
			alive, pid, err := daemonPIDFileLiveStrictWith(lookup, mtixDir)
			require.Error(t, err)
			assert.False(t, alive)
			assert.Zero(t, pid)
		})
	}
	t.Run("proven daemon is live", func(t *testing.T) {
		lookup := func(int) (procinfo.Proc, error) {
			return procinfo.Proc{Name: "mtix", Args: []string{"mtix", "daemon"}, Cwd: root}, nil
		}
		alive, pid, err := daemonPIDFileLiveStrictWith(lookup, mtixDir)
		require.NoError(t, err)
		assert.True(t, alive)
		assert.Equal(t, os.Getpid(), pid)
	})
}

func TestRegisterDaemonServiceStateDoctor_SpecErrors(t *testing.T) {
	run := func(specErr error) *service.DoctorResult {
		svc := service.NewDoctorService(service.DoctorOptions{})
		spec := func(int) (*serviceSpec, error) { return nil, specErr }
		require.NoError(t, registerDaemonServiceStateDoctorWith(svc, spec, func() (bool, int, error) { return false, 0, nil }, (&fakeManager{}).run))
		rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
		require.NoError(t, err)
		for i := range rows {
			if rows[i].ID == daemonServiceStoppedLiveID {
				return &rows[i]
			}
		}
		return nil
	}
	assert.Nil(t, run(fmt.Errorf("mtix daemon: %w", errUnsupportedServicePlatform)), "unsupported platform is a silent skip")
	row := run(fmt.Errorf("mtix daemon: resolve home: %w", errors.New("no home")))
	require.NotNil(t, row, "any other spec error surfaces")
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Contains(t, row.Detail, "resolve home")
	assert.Contains(t, row.Detail, "no home")
}
