// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestState_TargetParentComponent_RefusesBeforeCreation(t *testing.T) {
	for _, operation := range stateOperations() {
		t.Run(operation, func(t *testing.T) {
			root := safeFixtureRoot(t)
			input := root + "/component/../config"
			s := New(Env{GOOS: runtime.GOOS, Home: root, Values: map[string]string{"XDG_CONFIG_HOME": input}})
			requireLocationRefused(t, s, operation, filepath.Join(root, "config"), nil)
		})
	}
	root := safeFixtureRoot(t)
	_, err := openDirectory(root+"/component/../config", true, nil)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	requireMissingDirectory(t, filepath.Join(root, "config"))
}

func TestCanonicalLocation_DanglingAliasOrParentComponent_ReturnsLocationError(t *testing.T) {
	root := safeFixtureRoot(t)
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Symlink(filepath.Join(root, "absent"), alias))
	_, err := canonicalLocation(alias)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.False(t, missingState(err))
	_, err = canonicalLocation(root + "/component/../config")
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.False(t, missingState(err))
}

func TestDir_HomeOrAppDataParentComponent_Refuses(t *testing.T) {
	for _, env := range []Env{
		{GOOS: "linux", Home: "/home/component/../operator"},
		{GOOS: "windows", Values: map[string]string{"APPDATA": `C:\Users\component\..\operator`}},
	} {
		_, err := Dir(env)
		require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	}
}
