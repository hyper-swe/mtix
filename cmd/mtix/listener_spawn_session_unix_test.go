// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The detached child must lead its own session, so the command that started
// it can end without taking it down; spawnDetached builds its command here.
func TestNewDetachedCmd_OwnSessionNullStreamsProjectDir(t *testing.T) {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	require.NoError(t, err)
	defer func() { require.NoError(t, null.Close()) }()
	cmd := newDetachedCmd("/bin/echo", "/work/proj", []string{"a", "b"}, null)
	require.NotNil(t, cmd.SysProcAttr, "the child must be detached")
	assert.True(t, cmd.SysProcAttr.Setsid)
	assert.Equal(t, "/work/proj", cmd.Dir)
	assert.Equal(t, null, cmd.Stdin)
	assert.Equal(t, null, cmd.Stdout)
	assert.Equal(t, null, cmd.Stderr)
	assert.Equal(t, []string{"/bin/echo", "a", "b"}, cmd.Args)
}
