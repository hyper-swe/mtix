// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func approvalIndexFixture(version uint32, name, extension string) []byte {
	data := make([]byte, 12)
	copy(data, "DIRC")
	binary.BigEndian.PutUint32(data[4:8], version)
	if name != "" {
		binary.BigEndian.PutUint32(data[8:12], 1)
		entry := make([]byte, 62)
		binary.BigEndian.PutUint16(entry[60:62], uint16(len(name)))
		if version == 4 {
			entry = append(entry, 0)
		}
		entry = append(entry, []byte(name)...)
		entry = append(entry, 0)
		if version != 4 {
			for len(entry)%8 != 0 {
				entry = append(entry, 0)
			}
		}
		data = append(data, entry...)
	}
	if extension != "" {
		data = append(data, []byte(extension)...)
		data = append(data, 0, 0, 0, 0)
	}
	return approvalIndexChecksum(data)
}

func approvalIndexChecksum(body []byte) []byte {
	checksum := sha1.Sum(body) //nolint:gosec // Git index format checksum.
	return append(body, checksum[:]...)
}

func TestApprovalIndex_OrdinaryFormats_ReportSelectedPath(t *testing.T) {
	for _, version := range []uint32{2, 3, 4} {
		for _, name := range []string{"", "other", ".mtix/hooks.trust", ".MTIX/HOOKS.TRUST", ".mtix/"} {
			t.Run(fmt.Sprintf("v%d/%s", version, name), func(t *testing.T) {
				tracked, err := indexContainsApproval(approvalIndexFixture(version, name, "TREE"))
				require.NoError(t, err)
				require.Equal(t, name != "" && name != "other", tracked)
			})
		}
	}
}

func TestApprovalIndex_OtherFormats_ReturnError(t *testing.T) {
	tests := map[string][]byte{
		"header": []byte("invalid"), "version": approvalIndexFixture(5, "", ""),
		"split": approvalIndexFixture(2, "", "link"), "sparse": approvalIndexFixture(3, "", "sdir"),
		"other extension": approvalIndexFixture(2, "", "AAAA"),
		"absolute path":   approvalIndexFixture(2, "/other", ""),
	}
	checksum := approvalIndexFixture(2, "", "")
	checksum[len(checksum)-1] ^= 1
	tests["checksum"] = checksum
	count := make([]byte, 12)
	copy(count, "DIRC")
	binary.BigEndian.PutUint32(count[4:8], 2)
	binary.BigEndian.PutUint32(count[8:12], 200001)
	tests["count"] = approvalIndexChecksum(count)
	truncated := make([]byte, 12)
	copy(truncated, "DIRC")
	binary.BigEndian.PutUint32(truncated[4:8], 2)
	binary.BigEndian.PutUint32(truncated[8:12], 1)
	tests["entry"] = approvalIndexChecksum(truncated)
	for name, data := range tests {
		t.Run(name, func(t *testing.T) { _, err := indexContainsApproval(data); require.Error(t, err) })
	}
}

func TestApprovalIndex_Locations_RequireReadableDirectoryIndex(t *testing.T) {
	for _, input := range []string{"absent", "missing index", "git file", "directory index", "ordinary"} {
		t.Run(input, func(t *testing.T) {
			root := t.TempDir()
			git := filepath.Join(root, ".git")
			switch input {
			case "git file":
				require.NoError(t, os.WriteFile(git, []byte("gitdir: somewhere"), 0600))
			case "missing index", "directory index", "ordinary":
				require.NoError(t, os.Mkdir(git, 0700))
				if input == "directory index" {
					require.NoError(t, os.Mkdir(filepath.Join(git, "index"), 0700))
				}
				if input == "ordinary" {
					require.NoError(t, os.WriteFile(filepath.Join(git, "index"), approvalIndexFixture(2, "other", ""), 0600))
				}
			}
			tracked, _ := legacyTracked(root)
			require.Equal(t, input != "absent" && input != "ordinary", tracked)
		})
	}
}

func TestApprovalIndex_PrefixAndExtensions_ValidateLengths(t *testing.T) {
	for _, input := range [][]byte{nil, {0x80}, {0xff, 0xff, 0xff, 0xff, 0x7f}} {
		_, _, err := indexPrefix(input, 0)
		require.Error(t, err)
	}
	value, next, err := indexPrefix([]byte{1}, 0)
	require.NoError(t, err)
	require.Equal(t, 1, value)
	require.Equal(t, 1, next)
	require.Error(t, approvalIndexExtensions([]byte("TREE")))
	require.Error(t, approvalIndexExtensions([]byte{'T', 'R', 'E', 'E', 0, 0, 0, 1}))
}
