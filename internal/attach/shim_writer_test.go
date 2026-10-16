// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package attach

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func acceptTarget(target string, _ int) (string, error) { return target, nil }

func TestShimWriterInstall_ValidTarget_WritesShimAndRecord(t *testing.T) {
	st := newMemoryStore("/home/op/.config/mtix")
	w := NewShimWriter(st, 2, acceptTarget)
	path, err := w.Install(context.Background(), "/opt/pkg/bin/mtix", 501)
	require.NoError(t, err)
	assert.Equal(t, "/home/op/.config/mtix/mtix-shim", path)
	want, err := ShimScript(ShimRecord{Target: "/opt/pkg/bin/mtix", UID: 501, Protocol: 2})
	require.NoError(t, err)
	assert.Equal(t, want, st.execs[ShimName])
	status, err := w.Check(context.Background())
	require.NoError(t, err)
	assert.Equal(t, ArtifactCurrent, status.State)
	assert.Equal(t, path, status.Path)
}

func TestShimWriterInstall_Refusals_NothingWritten(t *testing.T) {
	tests := []struct {
		name     string
		dir      string
		validate func(string, int) (string, error)
	}{
		{"operator path with a space", "/Users/an op/.config/mtix", acceptTarget},
		{"refused target", "/home/op/.config/mtix", func(string, int) (string, error) {
			return "", ErrShimTargetRefused
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newMemoryStore(tt.dir)
			_, err := NewShimWriter(st, 1, tt.validate).Install(context.Background(), "/opt/mtix", 501)
			require.Error(t, err)
			assert.Empty(t, st.execs)
			assert.Empty(t, st.records)
		})
	}
}

func TestShimWriterCheck_States_ReportedForDoctor(t *testing.T) {
	tests := []struct {
		name     string
		prepare  func(st *memoryStore)
		validate func(string, int) (string, error)
		want     ArtifactState
	}{
		{"nothing installed", func(*memoryStore) {}, acceptTarget, ArtifactAbsent},
		{"older protocol bytes", func(st *memoryStore) {
			installAt(t, st, 1)
		}, acceptTarget, ArtifactStale},
		{"shim deleted, record kept", func(st *memoryStore) {
			installAt(t, st, 2)
			delete(st.execs, ShimName)
		}, acceptTarget, ArtifactStale},
		{"edited shim bytes", func(st *memoryStore) {
			installAt(t, st, 2)
			st.execs[ShimName] = []byte("#!/bin/sh\nexec /tmp/evil \"$@\"\n")
		}, acceptTarget, ArtifactStale},
		{"shim without its record", func(st *memoryStore) {
			st.execs[ShimName] = []byte("#!/bin/sh\n")
		}, acceptTarget, ArtifactRefused},
		{"target now refused", func(st *memoryStore) {
			installAt(t, st, 2)
		}, func(string, int) (string, error) { return "", ErrShimTargetRefused }, ArtifactRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newMemoryStore("/home/op/.config/mtix")
			tt.prepare(st)
			status, err := NewShimWriter(st, 2, tt.validate).Check(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tt.want, status.State, status.Detail)
		})
	}
}

func TestShimWriterWrite_StaleShim_RewrittenAtCurrentProtocol(t *testing.T) {
	st := newMemoryStore("/home/op/.config/mtix")
	installAt(t, st, 1)
	w := NewShimWriter(st, 2, acceptTarget)
	require.NoError(t, w.Write(context.Background()))
	want, err := ShimScript(ShimRecord{Target: "/opt/pkg/bin/mtix", UID: 501, Protocol: 2})
	require.NoError(t, err)
	assert.Equal(t, want, st.execs[ShimName])
	var rec ShimRecord
	_, err = st.ReadJSON(shimRecordName, &rec)
	require.NoError(t, err)
	assert.Equal(t, ShimRecord{Target: "/opt/pkg/bin/mtix", UID: 501, Protocol: 2}, rec, "only the protocol changes")
}

func TestShimWriterWrite_Failures_Reported(t *testing.T) {
	t.Run("no record to rewrite from", func(t *testing.T) {
		st := newMemoryStore("/home/op/.config/mtix")
		err := NewShimWriter(st, 2, acceptTarget).Write(context.Background())
		assert.ErrorIs(t, err, ErrShimNotInstalled)
	})
	t.Run("sandboxed operator state", func(t *testing.T) {
		st := newMemoryStore("/home/op/.config/mtix")
		installAt(t, st, 1)
		st.writeErr = errSandboxed
		err := NewShimWriter(st, 2, acceptTarget).Write(context.Background())
		assert.True(t, errors.Is(err, errSandboxed))
	})
}

func installAt(t *testing.T, st *memoryStore, protocol int) {
	t.Helper()
	_, err := NewShimWriter(st, protocol, acceptTarget).Install(context.Background(), "/opt/pkg/bin/mtix", 501)
	require.NoError(t, err)
}
