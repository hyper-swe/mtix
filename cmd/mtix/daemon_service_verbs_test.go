// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeManager is an injected service runner: no real launchctl, systemctl or schtasks runs.
// failing maps a joined argv prefix to the error that command returns; status answers the
// Status command with output and err. Every command is recorded.
type fakeManager struct {
	ran     []string
	failing map[string]error
	status  func() (string, error)
	onRun   func(argv string)
}

func (f *fakeManager) run(_ context.Context, out io.Writer, argv []string) error {
	joined := strings.Join(argv, " ")
	f.ran = append(f.ran, joined)
	if f.onRun != nil {
		f.onRun(joined)
	}
	if f.status != nil && isStatusArgv(joined) {
		text, err := f.status()
		_, _ = io.WriteString(out, text)
		return err
	}
	for prefix, err := range f.failing {
		if strings.HasPrefix(joined, prefix) {
			return err
		}
	}
	return nil
}

func isStatusArgv(joined string) bool {
	return strings.HasPrefix(joined, "launchctl print") || strings.HasPrefix(joined, "systemctl --user status") ||
		strings.HasPrefix(joined, "schtasks /Query")
}

var allServiceOSes = []string{"darwin", "linux", "windows"}

func TestStopDaemonService_RunsTheHoldingSequencePerOS(t *testing.T) {
	for _, goos := range allServiceOSes {
		t.Run(goos, func(t *testing.T) {
			spec := specFor(t, goos)
			fm := &fakeManager{}
			require.NoError(t, stopDaemonService(t.Context(), io.Discard, spec, fm.run))
			assert.Equal(t, joinCmds(spec.Stop), fm.ran)
		})
	}
}

func TestStopDaemonService_NothingInstalled_IsLenientPerOS(t *testing.T) {
	for _, goos := range allServiceOSes {
		t.Run(goos, func(t *testing.T) {
			spec := specFor(t, goos)
			last := joinCmds(spec.Stop)[len(spec.Stop)-1]
			fm := &fakeManager{
				failing: map[string]error{last: exitErr{code: 1}},
				status:  func() (string, error) { return "", exitErr{code: spec.NotLoadedExit} },
			}
			var out bytes.Buffer
			require.NoError(t, stopDaemonService(t.Context(), &out, spec, fm.run))
			assert.Contains(t, out.String(), "not loaded")
		})
	}
}

func TestStopDaemonService_RealFailure_StillFailsPerOS(t *testing.T) {
	for _, goos := range allServiceOSes {
		for name, status := range map[string]error{
			"still-loaded":    nil,
			"other-exit-code": exitErr{code: 7},
			"manager-missing": &os.PathError{Op: "exec", Path: "x", Err: os.ErrNotExist},
		} {
			t.Run(goos+"/"+name, func(t *testing.T) {
				spec := specFor(t, goos)
				last := joinCmds(spec.Stop)[len(spec.Stop)-1]
				boom := errors.New("permission denied")
				fm := &fakeManager{failing: map[string]error{last: boom}, status: func() (string, error) { return "", status }}
				err := stopDaemonService(t.Context(), io.Discard, spec, fm.run)
				require.ErrorIs(t, err, boom)
			})
		}
	}
}

func TestUninstallDaemonService_NothingInstalled_IsLenientPerOS(t *testing.T) {
	for _, goos := range allServiceOSes {
		t.Run(goos, func(t *testing.T) {
			spec := specFor(t, goos)
			spec.Path = "" // nothing on disk
			last := joinCmds(spec.Unregister)[len(spec.Unregister)-1]
			fm := &fakeManager{
				failing: map[string]error{last: exitErr{code: 1}},
				status:  func() (string, error) { return "", exitErr{code: spec.NotLoadedExit} },
			}
			require.NoError(t, uninstallDaemonService(t.Context(), io.Discard, spec, fm.run))
		})
	}
}

func TestUninstallDaemonService_RealError_FailsAndKeepsUnit(t *testing.T) {
	for _, goos := range allServiceOSes {
		t.Run(goos, func(t *testing.T) {
			spec := specFor(t, goos)
			spec.Path = filepath.Join(t.TempDir(), "unit")
			require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
			last := joinCmds(spec.Unregister)[len(spec.Unregister)-1]
			boom := errors.New("access denied")
			fm := &fakeManager{failing: map[string]error{last: boom}, status: func() (string, error) { return "state = running", nil }}
			require.ErrorIs(t, uninstallDaemonService(t.Context(), io.Discard, spec, fm.run), boom)
			assert.FileExists(t, spec.Path, "a unit whose service may still be registered is kept")
		})
	}
}

func TestUninstallDaemonService_RemovesUnitAndRunsSequence(t *testing.T) {
	spec := specFor(t, "darwin")
	spec.Path = filepath.Join(t.TempDir(), "x.plist")
	require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
	fm := &fakeManager{}
	require.NoError(t, uninstallDaemonService(t.Context(), io.Discard, spec, fm.run))
	assert.Equal(t, joinCmds(spec.Unregister), fm.ran)
	assert.NoFileExists(t, spec.Path)
	require.NoError(t, uninstallDaemonService(t.Context(), io.Discard, spec, fm.run), "a second uninstall converges")
}

func TestStartDaemonService_ToleratesAlreadyLoadedButFailsOnFinalCommand(t *testing.T) {
	spec := specFor(t, "darwin")
	fm := &fakeManager{failing: map[string]error{"launchctl bootstrap": exitErr{code: 5}}}
	require.NoError(t, startDaemonService(t.Context(), io.Discard, spec, fm.run), "bootstrap of an already-loaded job may fail")
	assert.Equal(t, joinCmds(spec.Start), fm.ran)

	boom := exitErr{code: 113}
	fm = &fakeManager{failing: map[string]error{"launchctl kickstart": boom}}
	require.ErrorIs(t, startDaemonService(t.Context(), io.Discard, spec, fm.run), boom, "a service that is not installed cannot start")
}

func TestStatusDaemonService_ClassifiesStatePerOS(t *testing.T) {
	notExec := &os.PathError{Op: "exec", Path: "x", Err: os.ErrNotExist}
	tests := []struct {
		name, goos, output string
		err                error
		wantRunning        bool
		wantOther          error
	}{
		{"darwin running", "darwin", "state = running\npid = 41", nil, true, nil},
		{"darwin loaded but waiting", "darwin", "state = waiting", nil, false, nil},
		{"darwin not loaded", "darwin", "", exitErr{code: 113}, false, nil},
		{"linux active", "linux", "Active: active (running)", nil, true, nil},
		{"linux inactive", "linux", "inactive (dead)", exitErr{code: 3}, false, nil},
		{"linux no unit", "linux", "", exitErr{code: 4}, false, nil},
		{"windows running", "windows", `"mtix-daemon-x","N/A","Running"`, nil, true, nil},
		{"windows disabled", "windows", `"mtix-daemon-x","N/A","Disabled"`, nil, false, nil},
		{"windows missing", "windows", "", exitErr{code: 1}, false, nil},
		{"manager missing", "linux", "", notExec, false, notExec},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm := &fakeManager{status: func() (string, error) { return tt.output, tt.err }}
			var out bytes.Buffer
			err := statusDaemonService(t.Context(), &out, specFor(t, tt.goos), fm.run)
			switch {
			case tt.wantOther != nil:
				require.ErrorIs(t, err, tt.wantOther)
				assert.NotErrorIs(t, err, errDaemonServiceNotRunning)
			case tt.wantRunning:
				require.NoError(t, err)
				assert.Contains(t, out.String(), "running")
			default:
				require.ErrorIs(t, err, errDaemonServiceNotRunning)
				assert.Contains(t, out.String(), "stopped")
			}
		})
	}
}

// TestDaemonService_StopThenStatusThenStart_FollowsTheManagerState drives a stateful darwin
// manager: bootout unloads, KeepAlive restarts only a loaded job, and status follows.
func TestDaemonService_StopThenStatusThenStart_FollowsTheManagerState(t *testing.T) {
	spec := specFor(t, "darwin")
	loaded := true
	fm := &fakeManager{status: func() (string, error) {
		if loaded {
			return "state = running", nil
		}
		return "", exitErr{code: 113}
	}}
	fm.onRun = func(argv string) {
		switch {
		case strings.HasPrefix(argv, "launchctl bootout"):
			loaded = false
		case strings.HasPrefix(argv, "launchctl bootstrap"):
			loaded = true
		}
	}
	require.NoError(t, statusDaemonService(t.Context(), io.Discard, spec, fm.run))
	require.NoError(t, stopDaemonService(t.Context(), io.Discard, spec, fm.run))
	require.ErrorIs(t, statusDaemonService(t.Context(), io.Discard, spec, fm.run), errDaemonServiceNotRunning, "stopped stays stopped")
	require.NoError(t, stopDaemonService(t.Context(), io.Discard, spec, fm.run), "a second stop converges")
	require.NoError(t, startDaemonService(t.Context(), io.Discard, spec, fm.run))
	require.NoError(t, statusDaemonService(t.Context(), io.Discard, spec, fm.run))
}

func TestDaemonServiceCmds_StopAndUninstall_UseTheInjectedRunner(t *testing.T) {
	spec := specFor(t, "linux")
	spec.Path = ""
	fm := &fakeManager{}
	cmds := newDaemonServiceCmdsWith(serviceDeps{run: fm.run, spec: func(int) (*serviceSpec, error) { return spec, nil }})
	byName := map[string]bool{}
	for _, c := range cmds {
		byName[c.Name()] = true
		if c.Name() != "stop" && c.Name() != "uninstall" {
			continue
		}
		var out bytes.Buffer
		c.SetOut(&out)
		c.SetContext(t.Context())
		require.NoError(t, c.RunE(c, nil))
		assert.Contains(t, out.String(), "$ systemctl --user disable --now")
	}
	assert.True(t, byName["stop"] && byName["uninstall"] && byName["start"] && byName["status"] && byName["install"])
}

// A failed non-final command while the service is still loaded must surface (MTIX-275 review S2-1):
// only the idempotency pre-clean (schtasks /End) may be forgiven without proof.
func TestStopAndUninstall_NonFinalFailureWhileLoaded_FailsPerOS(t *testing.T) {
	for _, goos := range allServiceOSes {
		for _, verb := range []string{"stop", "uninstall"} {
			spec := specFor(t, goos)
			cmds := spec.Stop
			if verb == "uninstall" {
				cmds = spec.Unregister
			}
			for i, argv := range cmds[:len(cmds)-1] {
				joined := strings.Join(argv, " ")
				if strings.HasPrefix(joined, "schtasks /End") {
					continue
				}
				t.Run(goos+"/"+verb+"/"+joined, func(t *testing.T) {
					spec.Path = filepath.Join(t.TempDir(), "unit")
					require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
					boom := errors.New("stop job failed")
					fm := &fakeManager{failing: map[string]error{joined: boom}, status: func() (string, error) { return "state = running", nil }}
					var err error
					if verb == "stop" {
						err = stopDaemonService(t.Context(), io.Discard, spec, fm.run)
					} else {
						err = uninstallDaemonService(t.Context(), io.Discard, spec, fm.run)
					}
					require.ErrorIs(t, err, boom, "command %d", i)
					assert.FileExists(t, spec.Path, "the unit file is kept while the service may still run")
				})
			}
		}
	}
}

// A non-final failure with a not-loaded proof is the idempotent case and is forgiven.
func TestStopAndUninstall_NonFinalFailureNotLoaded_IsForgivenPerOS(t *testing.T) {
	for _, goos := range allServiceOSes {
		spec := specFor(t, goos)
		spec.Path = ""
		for _, cmds := range [][][]string{spec.Stop, spec.Unregister} {
			joined := strings.Join(cmds[0], " ")
			fm := &fakeManager{failing: map[string]error{joined: exitErr{code: 1}}, status: func() (string, error) { return "", exitErr{code: spec.NotLoadedExit} }}
			require.NoError(t, runForgivingNotLoaded(t.Context(), io.Discard, spec, cmds, fm.run), goos)
		}
	}
}

// On Windows, schtasks /End exits nonzero when the task is installed but no instance runs.
// That failure carries no information and must not fail stop or uninstall (MTIX-275.3).
func TestStopAndUninstall_WindowsEndFailsWhileInstalledNotRunning_Succeeds(t *testing.T) {
	for _, verb := range []string{"stop", "uninstall"} {
		t.Run(verb, func(t *testing.T) {
			spec := specFor(t, "windows")
			spec.Path = filepath.Join(t.TempDir(), "unit")
			require.NoError(t, os.WriteFile(spec.Path, []byte("unit"), 0o600))
			fm := &fakeManager{
				failing: map[string]error{"schtasks /End": exitErr{code: 1}},
				status:  func() (string, error) { return `"mtix","N/A","Ready"`, nil },
			}
			var out bytes.Buffer
			var err error
			want := joinCmds(spec.Stop)
			if verb == "stop" {
				err = stopDaemonService(t.Context(), &out, spec, fm.run)
			} else {
				want = joinCmds(spec.Unregister)
				err = uninstallDaemonService(t.Context(), &out, spec, fm.run)
			}
			require.NoError(t, err)
			assert.Equal(t, want, fm.ran, "every command runs after the failed /End")
			assert.NotContains(t, out.String(), "not loaded", "the /End failure is forgiven by design, not by a not-loaded proof")
			if verb == "uninstall" {
				assert.NoFileExists(t, spec.Path, "the unit file is removed")
			}
		})
	}
}

// Only the schtasks /End idempotency pre-clean is forgiven without proof (MTIX-275.3).
func TestBestEffortServiceCmd_OnlySchtasksEnd(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want bool
	}{
		{"schtasks end", []string{"schtasks", "/End", "/TN", "x"}, true},
		{"schtasks end lowercase", []string{"schtasks", "/end", "/TN", "x"}, true},
		{"schtasks delete", []string{"schtasks", "/Delete", "/F", "/TN", "x"}, false},
		{"schtasks change", []string{"schtasks", "/Change", "/TN", "x", "/DISABLE"}, false},
		{"launchctl bootout", []string{"launchctl", "bootout", "x"}, false},
		{"other binary with /End", []string{"systemctl", "/End"}, false},
		{"single element", []string{"schtasks"}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, bestEffortServiceCmd(tt.argv))
		})
	}
}
