// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
)

func doctorUnionOperatorSnapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		data, readErr := os.ReadFile(path) //nolint:gosec // owned synthetic operator directory
		if readErr != nil {
			return readErr
		}
		files[rel] = string(data)
		return nil
	})
	require.NoError(t, err)
	return files
}

func TestDoctorAttachPermissionUnion_ExistingEmptyOperatorDirectoryStaysEmpty(t *testing.T) {
	for _, flags := range [][]string{{"--json"}, {"--json", "--fix"}} {
		t.Run(flags[len(flags)-1], func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			_, dir := permissionDraftState(t)
			require.NoError(t, os.MkdirAll(dir, 0700))
			before := doctorUnionOperatorSnapshot(t, dir)
			require.Empty(t, before)
			out := doctorCLIExecute(t, root, "", append([]string{"doctor"}, flags...)...)
			require.Equal(t, 0, out.exit, out.stderr)
			doctorCLIRows(t, out.stdout)
			assert.Equal(t, before, doctorUnionOperatorSnapshot(t, dir), "doctor must not mint a missing host identity")
		})
	}
}

func TestDoctorAttachPermissionUnion_PlainDoctorKeepsStaleShimUntilExplicitFix(t *testing.T) {
	root := doctorCLIBoard(t, "")
	st, dir := permissionDraftState(t)
	shimPath, target := installTestShim(t, st)
	_, err := st.WriteExecutable(attach.ShimName, []byte("#!/bin/sh\n# synthetic stale shim\n"))
	require.NoError(t, err)
	before := doctorUnionOperatorSnapshot(t, dir)
	out := doctorCLIExecute(t, root, "", "doctor", "--json")
	require.Equal(t, 0, out.exit, out.stderr)
	doctorCLIRows(t, out.stdout)
	assert.Equal(t, before, doctorUnionOperatorSnapshot(t, dir), "plain doctor must leave every operator file unchanged")
	fixed := doctorCLIExecute(t, root, "", "doctor", "--json", "--fix")
	require.Equal(t, 0, fixed.exit, fixed.stderr)
	want, err := attach.ShimScript(attach.ShimRecord{Target: target, UID: os.Getuid(), Protocol: attach.Protocol})
	require.NoError(t, err)
	got, err := os.ReadFile(shimPath) //nolint:gosec // owned synthetic shim path
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "the registered non-grant fixer must still repair the shim")
}

func TestDoctorAttachPermissionUnion_OrdinaryCommandStillRefreshesShim(t *testing.T) {
	root := doctorCLIBoard(t, "")
	st, _ := permissionDraftState(t)
	shimPath, target := installTestShim(t, st)
	_, err := st.WriteExecutable(attach.ShimName, []byte("#!/bin/sh\n# synthetic stale shim\n"))
	require.NoError(t, err)
	out := doctorCLIExecute(t, root, "", "help")
	require.Equal(t, 0, out.exit, out.stderr)
	want, err := attach.ShimScript(attach.ShimRecord{Target: target, UID: os.Getuid(), Protocol: attach.Protocol})
	require.NoError(t, err)
	got, err := os.ReadFile(shimPath) //nolint:gosec // owned synthetic shim path
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "ordinary commands must retain main's automatic refresh")
}
