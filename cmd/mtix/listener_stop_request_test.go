// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func liveListenInputs() service.ContactInputs {
	return service.ContactInputs{Listen: service.ListenSettings{Hold: true}, LiveLease: true}
}

func TestDaemonListenerDone_StopRequested_ExitsEvenWithALiveLease(t *testing.T) {
	requested := false
	l := &daemonListener{
		load:          func() (service.ContactInputs, error) { return liveListenInputs(), nil },
		now:           time.Now,
		relay:         true,
		stopRequested: func() bool { return requested },
	}
	done, _ := l.Done()
	assert.False(t, done, "a live waiter keeps the listener while nothing asks it to stop")
	requested = true
	done, why := l.Done()
	assert.True(t, done)
	assert.Contains(t, why, "stop requested")
}

func TestDaemonListenerDone_NoStopHook_BehavesAsBefore(t *testing.T) {
	l := &daemonListener{load: func() (service.ContactInputs, error) { return liveListenInputs(), nil }, now: time.Now, relay: true}
	done, _ := l.Done()
	assert.False(t, done)
}

func TestListenerStopFile_RoundTrip_WriteSeeClear(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o700))
	assert.False(t, listenerStopRequested(dir))
	require.NoError(t, writeListenerStopRequest(dir))
	assert.True(t, listenerStopRequested(dir))
	info, err := os.Stat(filepath.Join(dir, "data", listenerStopFilename))
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	require.NoError(t, clearListenerStopRequest(dir))
	assert.False(t, listenerStopRequested(dir))
	require.NoError(t, clearListenerStopRequest(dir), "clearing an absent request is fine")
}

func TestListenerStopFile_SymlinkOrDirectory_IsRefused(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	require.NoError(t, os.MkdirAll(data, 0o700))
	target := filepath.Join(dir, "victim")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0o600))
	path := filepath.Join(data, listenerStopFilename)
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	require.Error(t, writeListenerStopRequest(dir), "a symlink at the request path is never followed")
	body, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, "keep", string(body))
	assert.False(t, listenerStopRequested(dir), "a symlink is not a request")
}

func TestListenerStopFile_StaleRequestIsDroppedAtStart(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o700))
	require.NoError(t, writeListenerStopRequest(dir))
	require.NoError(t, clearListenerStopRequest(dir), "a listener starting clears a request left by an earlier stop")
	assert.False(t, listenerStopRequested(dir))
}
