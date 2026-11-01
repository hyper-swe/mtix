// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests force the metadata/header interleaving of concurrent first opens.
package sqlite

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

type growingHeaderFile struct {
	*os.File
	grow func()
}

func (f *growingHeaderFile) ReadAt(p []byte, off int64) (int, error) {
	f.grow()
	return f.File.ReadAt(p, off)
}

func TestValidateDBFile_GrowthBetweenStatAndHeader_IsNotTruncation(t *testing.T) {
	s, source := newDurabilityTestStore(t)
	seedRows(t, s)
	require.NoError(t, s.Close())
	image, err := os.ReadFile(source)
	require.NoError(t, err)
	require.Greater(t, len(image), 4096)
	path := filepath.Join(t.TempDir(), "store.db")
	// A first opener has written the initial page. Before our header read it
	// finishes initialization and checkpoints the full database, removing WAL.
	require.NoError(t, os.WriteFile(path, image[:4096], 0o600))
	f, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })
	file := &growingHeaderFile{File: f, grow: func() {
		require.NoError(t, os.WriteFile(path, image, 0o600))
	}}
	require.NoError(t, validateOpenDBFile(path, file))
	require.NoError(t, validateDBFile(path))
}

type failingHeaderStat struct {
	*os.File
	calls  int
	failAt int
}

func (f *failingHeaderStat) Stat() (os.FileInfo, error) {
	f.calls++
	if f.calls == f.failAt {
		return nil, os.ErrPermission
	}
	return f.File.Stat()
}

func TestValidateOpenDBFile_StatFailure_IsReturned(t *testing.T) {
	s, path := newDurabilityTestStore(t)
	require.NoError(t, s.Close())
	for _, failAt := range []int{1, 2} {
		t.Run(fmt.Sprint(failAt), func(t *testing.T) {
			f, err := os.Open(path)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, f.Close()) })
			err = validateOpenDBFile(path, &failingHeaderStat{File: f, failAt: failAt})
			require.ErrorIs(t, err, os.ErrPermission)
		})
	}
}

func TestValidateDBFile_HeaderVariants_PreserveIntegrityGate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int
		pageSize  uint16
		stale     bool
		corrupted bool
		contains  string
	}{
		{name: "empty"},
		{name: "bad signature", size: 100, corrupted: true},
		{name: "stale count", size: 100, pageSize: 4096, stale: true},
		{name: "65536 byte pages", size: 100, pageSize: 1, corrupted: true},
		// 2 pages of 65536 bytes need 131072 bytes; a 70000-byte file is
		// truncated only if the page size is read as 65536 and not 32768.
		{name: "65536 byte pages between 32768 and 65536 multiples", size: 70000, pageSize: 1, corrupted: true},
		{name: "one byte under header", size: 99, pageSize: 4096, corrupted: true, contains: "smaller than a SQLite header"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, tc.size)
			if tc.pageSize != 0 {
				copy(data, "SQLite format 3\x00")
				binary.BigEndian.PutUint16(data[16:18], tc.pageSize)
				binary.BigEndian.PutUint32(data[28:32], 2)
				if tc.stale {
					binary.BigEndian.PutUint32(data[24:28], 1)
				}
			}
			path := filepath.Join(t.TempDir(), "store.db")
			require.NoError(t, os.WriteFile(path, data, 0o600))
			err := validateDBFile(path)
			if tc.corrupted {
				require.ErrorIs(t, err, model.ErrCorrupted)
				if tc.contains != "" {
					require.ErrorContains(t, err, tc.contains)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateDBFile_InvalidParent_ReturnsOpenError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	require.Error(t, validateDBFile(filepath.Join(path, "child")))
}

type unreadableHeader struct{ *os.File }

func (f *unreadableHeader) ReadAt([]byte, int64) (int, error) {
	return 0, os.ErrPermission
}

func TestValidateOpenDBFile_HeaderReadFailure_IsReturned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	require.NoError(t, os.WriteFile(path, make([]byte, 100), 0o600))
	f, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, f.Close()) })
	require.ErrorIs(t, validateOpenDBFile(path, &unreadableHeader{File: f}), os.ErrPermission)
}
