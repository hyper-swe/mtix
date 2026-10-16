// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendFile_ConcurrentFirstWritersToAMissingFileLoseNoLine(t *testing.T) {
	const writers = 32
	base := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	start := make(chan struct{})
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- deliverAppend(t, base, "events.log", Event{Name: EventNodeCreated, NodeID: fmt.Sprintf("HP-%d", i)}, `{}`)
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	data, err := os.ReadFile(filepath.Join(base, "events.log"))
	require.NoError(t, err)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	require.Len(t, lines, writers)
	seen := map[string]int{}
	for _, line := range lines {
		seen[strings.Split(line, "\t")[2]]++
	}
	for i := 0; i < writers; i++ {
		require.Equal(t, 1, seen[fmt.Sprintf("HP-%d", i)], "line for writer %d appears exactly once", i)
	}
}

func TestAppendFile_FormatCharactersInFieldsAreRefused(t *testing.T) {
	for name, id := range map[string]string{"zero width space": "HP-1\u200bx", "right-to-left override": "HP-1\u202ex", "soft hyphen": "HP-1\u00adx"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			require.Error(t, deliverAppend(t, base, "events.log", Event{Name: EventNodeCreated, NodeID: id}, `{}`))
		})
	}
}

func TestAppendFile_RawLineSeparatorsInThePayloadAreRefused(t *testing.T) {
	for name, body := range map[string]string{"line separator": "{\"a\":\"x\u2028y\"}", "paragraph separator": "{\"a\":\"x\u2029y\"}"} {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			require.Error(t, deliverAppend(t, base, "events.log", okEvent, body))
			_, err := os.Stat(filepath.Join(base, "events.log"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

func TestOpenAppendFile_ReturnsABlockingDescriptor(t *testing.T) {
	base := t.TempDir()
	f, err := openAppendFile(openRoot(t, base), "events.log", nil)
	require.NoError(t, err)
	defer f.Close()
	conn, err := f.SyscallConn()
	require.NoError(t, err)
	var flags uintptr
	var errno syscall.Errno
	require.NoError(t, conn.Control(func(fd uintptr) {
		flags, _, errno = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
	}))
	require.Zero(t, errno)
	require.Zero(t, flags&syscall.O_NONBLOCK, "the descriptor is back in blocking mode before any write")
}
