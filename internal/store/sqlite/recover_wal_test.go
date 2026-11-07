// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bytes"
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Fixed WAL format vectors: one 512-byte zero page, salts11223344/55667788.
// The checksums were computed independently from the documented file format.
func walFormatVector(t *testing.T, big bool) []byte {
	t.Helper()
	prefix := "377f0682002de2180000020000000000112233445566778818fe2524594f631e000000010000000111223344556677885088905a4fe4398a"
	if big {
		prefix = "377f0683002de218000002000000000011223344556677882726fe1b23664f5c000000010000000111223344556677881dca68813811d362"
	}
	data, err := hex.DecodeString(prefix)
	require.NoError(t, err)
	return append(data, make([]byte, 512)...)
}

// FR-19.1: WAL checksums support both documented input word orders.
func TestRecoverWAL_HeaderAndFrameVerification(t *testing.T) {
	for _, big := range []bool{false, true} {
		data := walFormatVector(t, big)
		require.NoError(t, verifyRecoverWAL(t.Context(), bytes.NewReader(data), int64(len(data))))
		require.NoError(t, verifyRecoverWAL(t.Context(), bytes.NewReader(data[:32]), 32))
		for _, tc := range []struct {
			name string
			at   int
			want string
		}{
			{"magic", 0, "magic"}, {"version", 4, "version"}, {"page-size", 8, "page size"},
			{"header-checksum", 24, "header checksum"}, {"header-checksum-second", 28, "header checksum"}, {"salt", 40, "salt"}, {"salt-second", 44, "salt"},
			{"frame-checksum", 48, "frame 0 checksum"}, {"frame-checksum-second", 52, "frame 0 checksum"}, {"page-data", 100, "frame 0 checksum"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				bad := bytes.Clone(data)
				bad[tc.at] ^= 1
				require.ErrorContains(t, verifyRecoverWAL(t.Context(), bytes.NewReader(bad), int64(len(bad))), tc.want)
			})
		}
		require.ErrorContains(t, verifyRecoverWAL(t.Context(), bytes.NewReader(data[:10]), 10), "header")
		require.ErrorContains(t, verifyRecoverWAL(t.Context(), bytes.NewReader(data[:32]), int64(len(data))), "read WAL frame")
		tail := append(bytes.Clone(data), byte(0))
		require.ErrorContains(t, verifyRecoverWAL(t.Context(), bytes.NewReader(tail), int64(len(tail))), "frame boundary")
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		require.ErrorIs(t, verifyRecoverWAL(ctx, bytes.NewReader(data), int64(len(data))), context.Canceled)
	}
}

// FR-19.1: absent and empty logs are ordinary; a non-file log cannot be verified.
func TestRecoverWAL_FileAvailability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.db")
	require.NoError(t, checkRecoverWAL(t.Context(), path))
	require.NoError(t, os.WriteFile(path+"-wal", nil, 0600))
	require.NoError(t, checkRecoverWAL(t.Context(), path))
	require.NoError(t, os.WriteFile(path+"-wal", walFormatVector(t, false), 0600))
	require.NoError(t, checkRecoverWAL(t.Context(), path))
	require.NoError(t, os.Remove(path+"-wal"))
	require.NoError(t, os.Mkdir(path+"-wal", 0700))
	require.ErrorContains(t, checkRecoverWAL(t.Context(), path), "regular file")
}
