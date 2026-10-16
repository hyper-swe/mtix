// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// installTestShim installs a shim for a private fake mtix through the real
// operator directory, as setup will.
func installTestShim(t *testing.T, st *oplocal.State) (shimPath, target string) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	target = filepath.Join(dir, "mtix")
	require.NoError(t, os.WriteFile(target, []byte("#!/bin/sh\n"), 0o700)) //nolint:gosec // fixture executable
	shimPath, err = attach.NewShimWriter(st, attach.Protocol, attach.ValidateShimTarget).Install(context.Background(), target, os.Getuid())
	require.NoError(t, err)
	return shimPath, target
}

// snapshotOperatorFiles reads every operator-local file except the attach
// artifacts a refresh may legitimately rewrite.
func snapshotOperatorFiles(t *testing.T, st *oplocal.State) map[string]string {
	t.Helper()
	dir, err := st.Path()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if name == attach.ShimName || strings.HasPrefix(name, "attach-") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // fixture directory
		require.NoError(t, err)
		out[name] = string(data)
	}
	return out
}

func TestRootPostRun_TamperedShim_RewrittenWithoutTTYAndGrantsUntouched(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	shimPath, target := installTestShim(t, st)
	require.NoError(t, st.WriteJSON("grants", map[string]any{"board_grants": []any{}}))
	require.NoError(t, st.WriteJSON("hook-approval-fixture", map[string]string{"digest": "abc"}))
	_, err := st.WriteExecutable(attach.ShimName, []byte("#!/bin/sh\nexec /tmp/other \"$@\"\n"))
	require.NoError(t, err)
	before := snapshotOperatorFiles(t, st)
	require.NotEmpty(t, before)

	t.Chdir(t.TempDir())
	root := newRootCmd()
	root.SetIn(strings.NewReader("")) // not a terminal
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"help"})
	require.NoError(t, root.ExecuteContext(context.Background()))

	want, err := attach.ShimScript(attach.ShimRecord{Target: target, UID: os.Getuid(), Protocol: attach.Protocol})
	require.NoError(t, err)
	got, err := os.ReadFile(shimPath) //nolint:gosec // fixture path
	require.NoError(t, err)
	assert.Equal(t, string(want), string(got), "the next command restores the release-defined bytes")
	assert.Equal(t, before, snapshotOperatorFiles(t, st), "grants and every other operator record stay byte-identical")
}

func TestRootPostRun_NothingInstalled_WritesNothing(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	t.Chdir(t.TempDir())
	root := newRootCmd()
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"help"})
	require.NoError(t, root.ExecuteContext(context.Background()))
	dir, err := st.Path()
	require.NoError(t, err)
	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err), "a host without attach artifacts gets no operator directory")
}

func TestAttachRefresh_ProductionRegistry_OnlyTheShim(t *testing.T) {
	st := testutil.OperatorState(t)
	reg, err := productionAttachRegistry(st, attach.Protocol)
	require.NoError(t, err)
	assert.NotNil(t, reg.Writer("shim"))
	for _, h := range attach.Harnesses() {
		assert.Nil(t, reg.Writer("glue."+h), "no glue writer ships in this build")
	}
}
