// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestAddToGitignore_FIFO_RefusesWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, unix.Mkfifo(filepath.Join(dir, ".gitignore"), 0600))
	done := make(chan error, 1)
	go func() { done <- addToGitignore(dir, ".mtix/hooks.trust") }()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(3 * time.Second):
		// Release the blocked opener so the goroutine ends, then fail.
		fd, err := unix.Open(filepath.Join(dir, ".gitignore"), unix.O_RDWR|unix.O_NONBLOCK, 0)
		if err == nil {
			require.NoError(t, unix.Close(fd))
		}
		<-done
		t.Fatal("addToGitignore blocked on a FIFO")
	}
}
