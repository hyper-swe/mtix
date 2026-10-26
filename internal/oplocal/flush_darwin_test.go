// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// These tests distinguish syscall dispatch and actual native return values from
// any hardware or sudden-power-loss guarantee.
func flushResults(t *testing.T, count *int, results []error) func(uintptr) error {
	t.Helper()
	return func(fd uintptr) error {
		require.Equal(t, uintptr(57), fd)
		index := *count
		*count++
		if index < len(results) {
			return results[index]
		}
		return nil
	}
}

func TestDarwinFlush_DispatchAndFallback_PreservesFailures(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		full, fallback                     []error
		want                               error
		fullCount, fallbackCount, warnings int
	}{
		{"full sync", []error{nil}, nil, nil, 1, 0, 0},
		{"unsupported", []error{unix.ENOTSUP}, []error{nil}, nil, 1, 1, 1},
		{"retry full", []error{unix.EINTR, unix.EINTR, nil}, nil, nil, 3, 0, 0},
		{"retry fallback", []error{unix.ENOTSUP}, []error{unix.EINTR, nil}, nil, 1, 2, 1},
		{"io error", []error{unix.EIO}, nil, unix.EIO, 1, 0, 0},
		{"bad descriptor", []error{unix.EBADF}, nil, unix.EBADF, 1, 0, 0},
		{"invalid operation", []error{unix.EINVAL}, nil, unix.EINVAL, 1, 0, 0},
		{"socket unsupported", []error{unix.EOPNOTSUPP}, nil, unix.EOPNOTSUPP, 1, 0, 0},
		{"fallback failure", []error{unix.ENOTSUP}, []error{unix.EIO}, unix.EIO, 1, 1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			full, fallback, warnings := 0, 0, 0
			calls := darwinFlushCalls{fullSync: flushResults(t, &full, tc.full), fileSync: flushResults(t, &fallback, tc.fallback), warn: func(err error) { require.ErrorIs(t, err, unix.ENOTSUP); warnings++ }}
			err := flushDarwinHandle(57, calls)
			if tc.want == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.want)
			}
			require.Equal(t, tc.fullCount, full)
			require.Equal(t, tc.fallbackCount, fallback)
			require.Equal(t, tc.warnings, warnings)
		})
	}
}

func TestWriteJSON_Darwin_UsesFullFsync(t *testing.T) {
	s := flushFixture(t)
	_, err := s.HostID()
	require.NoError(t, err)
	var directories []bool
	full, fallback, warnings := 0, 0, 0
	platform := darwinPlatformCalls(func(string, ...any) { warnings++ })
	requireDarwinPlatformBindings(t, platform)
	s.flushNative = func(f *os.File) error {
		stat, err := f.Stat()
		if err != nil {
			return err
		}
		directories = append(directories, stat.IsDir())
		calls := platform
		calls.fullSync = func(fd uintptr) error { full++; return platform.fullSync(fd) }
		calls.fileSync = func(fd uintptr) error { fallback++; return platform.fileSync(fd) }
		return withFileHandle(f, func(fd uintptr) error { return flushDarwinHandle(fd, calls) })
	}
	require.NoError(t, s.WriteJSON("hooks", "native flush observation"))
	require.Equal(t, []bool{false, true}, directories)
	require.Equal(t, 2, full)
	require.Equal(t, fallback, warnings)
	t.Logf("native Darwin: full-sync requests=%d raw-fsync fallback requests=%d warnings=%d; not a power-loss test", full, fallback, warnings)
}

func TestDarwinFlush_RawFallback_UsesRealDescriptor(t *testing.T) {
	f := flushTestFile(t)
	calls, warnings := 0, 0
	platform := darwinPlatformCalls(func(string, ...any) { warnings++ })
	requireDarwinPlatformBindings(t, platform)
	injected := platform
	injected.fullSync = func(uintptr) error { return unix.ENOTSUP }
	injected.fileSync = func(fd uintptr) error { calls++; return platform.fileSync(fd) }
	err := withFileHandle(f, func(fd uintptr) error { return flushDarwinHandle(fd, injected) })
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, 1, warnings)
}

func TestDarwinFlush_InvalidDescriptor_PreservesCause(t *testing.T) {
	for _, flush := range []func(uintptr) error{fullSyncFD, fileSyncFD} {
		require.ErrorIs(t, flush(^uintptr(0)), unix.EBADF)
	}
}

func requireDarwinPlatformBindings(t *testing.T, calls darwinFlushCalls) {
	t.Helper()
	require.NotNil(t, calls.fullSync)
	require.NotNil(t, calls.fileSync)
	require.Equal(t, reflect.ValueOf(fullSyncFD).Pointer(), reflect.ValueOf(calls.fullSync).Pointer(), "production full sync must select F_FULLFSYNC")
	require.Equal(t, reflect.ValueOf(fileSyncFD).Pointer(), reflect.ValueOf(calls.fileSync).Pointer(), "production fallback must select raw fsync")
}

func TestDarwinPlatformCalls_WarningClosure_RecordsError(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&output, nil))
	calls := darwinPlatformCalls(logger.Warn)
	calls.warn(unix.ENOTSUP)
	require.Equal(t, 1, bytes.Count(output.Bytes(), []byte("\n")))
	var event map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &event))
	require.Equal(t, "WARN", event["level"])
	require.Equal(t, "operator state full sync unsupported; attempting fsync", event["msg"])
	require.Equal(t, unix.ENOTSUP.Error(), event["error"])
	require.Len(t, event, 4)
}
