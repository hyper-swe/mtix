// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/hyper-swe/mtix/internal/model"
)

// These tests retain actual Windows handle mode, filesystem and flush results.
// Successful API returns are not a simulation of sudden power loss.
type windowsFlushObservation struct {
	Name         string `json:"name"`
	WriteThrough bool   `json:"write_through"`
	Filesystem   string `json:"filesystem"`
}

func observeWindowsFlush(f *os.File) (windowsFlushObservation, error) {
	var observation windowsFlushObservation
	var mode uint32
	var status windows.IO_STATUS_BLOCK
	handle := windows.Handle(f.Fd())
	// FILE_INFORMATION_CLASS FileModeInformation=16 returns FILE_MODE_INFORMATION.
	if err := windows.NtQueryInformationFile(handle, &status, (*byte)(unsafe.Pointer(&mode)), 4, 16); err != nil {
		return observation, err
	}
	observation.WriteThrough = mode&windows.FILE_WRITE_THROUGH != 0
	var path [32768]uint16
	if _, err := windows.GetFinalPathNameByHandle(handle, &path[0], 32768, 0); err != nil {
		return observation, err
	}
	observation.Name = filepath.Base(windows.UTF16ToString(path[:]))
	var filesystem [256]uint16
	if err := windows.GetVolumeInformationByHandle(handle, nil, 0, nil, nil, nil, &filesystem[0], 256); err != nil {
		return observation, err
	}
	observation.Filesystem = windows.UTF16ToString(filesystem[:])
	return observation, nil
}

func TestWindowsFlush_Delegate_PreservesHandleAndError(t *testing.T) {
	called := false
	err := flushWindowsHandle(57, func(handle windows.Handle) error {
		called = true
		require.Equal(t, windows.Handle(57), handle)
		return syscall.EIO
	})
	require.True(t, called)
	require.ErrorIs(t, err, syscall.EIO)
}

func TestWriteJSON_Windows_UsesWriteThrough(t *testing.T) {
	s := flushFixture(t)
	_, err := s.HostID()
	require.NoError(t, err)
	var observations []windowsFlushObservation
	successful := 0
	s.flushNative = func(f *os.File) error {
		observation, err := observeWindowsFlush(f)
		if err != nil {
			return err
		}
		observations = append(observations, observation)
		return withFileHandle(f, func(fd uintptr) error {
			return flushWindowsHandle(fd, func(handle windows.Handle) error {
				err := windows.FlushFileBuffers(handle)
				if err == nil {
					successful++
				}
				return err
			})
		})
	}
	require.NoError(t, s.WriteJSON("hooks", "native Windows observation"))
	require.Len(t, observations, 2)
	require.True(t, strings.HasPrefix(observations[0].Name, ".state-"))
	require.Equal(t, "hooks.json", observations[1].Name)
	for _, observation := range observations {
		require.True(t, observation.WriteThrough)
		require.NotEmpty(t, observation.Filesystem)
	}
	require.Equal(t, 2, successful)
	data, err := json.Marshal(map[string]any{"observations": observations, "successful_flush_file_buffers": successful, "power_loss_tested": false})
	require.NoError(t, err)
	t.Logf("DURABILITY_NATIVE_JSON %s", data)
}

func TestWriteJSON_FlushError_FailsWrite(t *testing.T) {
	for _, stage := range []int{1, 2} {
		name := "before publication"
		if stage == 2 {
			name = "after publication"
		}
		t.Run(name, func(t *testing.T) {
			s := flushFixture(t)
			require.NoError(t, s.WriteJSON("hooks", "original"))
			calls := 0
			s.flushNative = func(f *os.File) error {
				calls++
				if calls == stage {
					return syscall.EIO
				}
				return platformFlush(f)
			}
			err := s.WriteJSON("hooks", "replacement")
			require.Equal(t, stage, calls)
			require.ErrorIs(t, err, syscall.EIO)
			require.NotErrorIs(t, err, model.ErrOperatorStateUnreadable)
			var got string
			_, err = s.ReadJSON("hooks", &got)
			require.NoError(t, err)
			want := "original"
			if stage == 2 {
				want = "replacement"
			}
			require.Equal(t, want, got)
		})
	}
}
