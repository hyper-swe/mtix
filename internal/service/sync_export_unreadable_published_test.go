// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// A published tasks.json that cannot be read for a reason other than being
// missing must fail the automatic export with the wrapped read error and
// publish nothing: treating it as absent would overwrite a file the board
// cannot vouch for (FR-15.3, MTIX-130.20.1.1).
func TestPublishExport_AutomaticUnreadablePublishedFile_ReturnsWrappedErrorWritesNothing(t *testing.T) {
	f := newMarshalFixture(t)
	path := filepath.Join(f.dir, "tasks.json")
	// A directory at the published path makes the read fail with a
	// non-"not exist" error on every platform.
	require.NoError(t, os.Mkdir(path, 0755))
	marker := filepath.Join(path, "keep")
	require.NoError(t, os.WriteFile(marker, []byte("x"), 0600))

	marshals := 0
	f.svc.marshalExport = func(*sqlite.ExportData) ([]byte, error) {
		marshals++
		return nil, nil
	}
	// AutoExport's guard reads the file first and catches an unreadable path
	// before publication, so this branch covers the window after that guard
	// read; call publishExport directly. It must not treat an unreadable file as absent.
	err := f.svc.publishExport(context.Background(), f.dir, *f.now, true)

	require.Error(t, err)
	require.ErrorContains(t, err, "read published export")
	var pathErr *fs.PathError
	require.ErrorAs(t, err, &pathErr, "the read error must stay in the error chain")
	require.Equal(t, path, pathErr.Path)
	require.Zero(t, marshals, "nothing may be serialized for a board whose published file cannot be read")
	info, statErr := os.Stat(path)
	require.NoError(t, statErr)
	require.True(t, info.IsDir(), "the unreadable published path must be left untouched")
	_, markerErr := os.Stat(marker)
	require.NoError(t, markerErr)
}
