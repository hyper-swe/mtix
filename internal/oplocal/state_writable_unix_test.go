// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
//go:build !windows

package oplocal

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestProbeWritable_WritableDirectory_LeavesNoFiles(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	dir, err := s.Path()
	require.NoError(t, err)
	before, err := os.ReadDir(dir)
	require.NoError(t, err)

	require.NoError(t, s.ProbeWritable())

	after, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, after, len(before), "the probe file must be removed")
}

func TestProbeWritable_UnwritableDirectory_ReturnsOperatorStateError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	s := fixture(t)
	require.NoError(t, s.Ensure())
	dir, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })

	err = s.ProbeWritable()

	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestProbeWritable_NoOperatorDirectoryAndUnwritableParent_ReturnsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	s := fixture(t)
	dir, err := s.Path()
	require.NoError(t, err)
	parent := dir[:len(dir)-len("/mtix")]
	require.NoError(t, os.MkdirAll(parent, 0o700))
	require.NoError(t, os.Chmod(parent, 0o500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(parent, 0o700)) })

	require.Error(t, s.ProbeWritable())
}

// The probe must really create a file: a refusing create seam fails it even though the
// directory validates, and a directory that fails validation never reaches the create.
func TestProbeWritable_CreateRefused_ReturnsOperatorStateError(t *testing.T) {
	s := fixture(t)
	calls := 0
	s.probeCreate = func(string, string) (*os.File, error) {
		calls++
		return nil, os.ErrPermission
	}
	err := s.ProbeWritable()
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.ErrorIs(t, err, os.ErrPermission)
	require.Equal(t, 1, calls)
}

func TestProbeWritable_DirectoryRefused_NeverCreatesFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	s := fixture(t)
	require.NoError(t, s.Ensure())
	dir, err := s.Path()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
	calls := 0
	s.probeCreate = func(string, string) (*os.File, error) { calls++; return nil, os.ErrPermission }
	require.Error(t, s.ProbeWritable())
	require.Zero(t, calls, "a refused directory ends the probe before any create")
}
