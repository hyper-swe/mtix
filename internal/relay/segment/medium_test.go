// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package segment_test

import (
	"errors"
	"fmt"
	"io/fs"
	"syscall"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

func TestMediumError_ClassifiesIOAndPermissionNotNotExist(t *testing.T) {
	path := "/abs/relay/peers/p/segments/x"
	tests := []struct {
		name  string
		cause error
		class string
	}{
		{"permission", &fs.PathError{Op: "open", Path: path, Err: syscall.EACCES}, "permission denied"},
		{"io error", &fs.PathError{Op: "read", Path: path, Err: syscall.EIO}, "input/output error"},
		{"unknown", errors.New("odd"), "I/O error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := segment.MediumError("peers/p/segments/x", fmt.Errorf("open: %w", tt.cause))
			require.ErrorIs(t, err, segment.ErrMediumUnreadable)
			require.ErrorIs(t, err, tt.cause, "the OS error stays reachable")
			require.Equal(t, "RELAY_MEDIUM_UNREADABLE", segment.CodeOf(err))
			require.NotEqual(t, segment.CodeSegmentCorrupt, segment.CodeOf(err))
			require.Equal(t, "RELAY_MEDIUM_UNREADABLE: cannot read peers/p/segments/x: "+tt.class, err.Error())
			require.NotContains(t, err.Error(), "/abs/")
		})
	}
}

func TestMediumError_PassesThroughNotExistVerdictsAndNil(t *testing.T) {
	require.NoError(t, segment.MediumError("x", nil))
	nf := &fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist}
	require.Equal(t, error(nf), segment.MediumError("x", nf))
	require.Empty(t, segment.CodeOf(segment.MediumError("x", nf)))
	sym := fmt.Errorf("%w: x", segment.ErrSymlink)
	require.Equal(t, segment.CodeSymlink, segment.CodeOf(segment.MediumError("x", sym)))
	gap := fmt.Errorf("%w", segment.ErrGap)
	require.Equal(t, segment.CodeGap, segment.CodeOf(segment.MediumError("x", gap)))
}
