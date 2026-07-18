// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-54: filesystem-safety preflight. SQLite WAL needs true shared-memory
// (the -shm wal-index) + faithful POSIX locking, which FUSE/network filesystems
// do not provide — the 2026-07-11 corruption. These tests pin the classifier,
// the Linux magic map, and the refuse/allow decision (all pure, cross-platform).

func TestClassifyFSName(t *testing.T) {
	cases := []struct {
		name  string
		class fsClass
	}{
		// FUSE — the exact corruption vector.
		{"fuse", fsFuse},
		{"macfuse", fsFuse},
		{"osxfuse", fsFuse},
		{"fuseblk", fsFuse},
		{"fuse.sshfs", fsFuse},
		{"FUSE", fsFuse}, // case-insensitive
		{"sshfs", fsFuse},
		{"gocryptfs", fsFuse},
		{"s3fs", fsFuse},
		// Network.
		{"nfs", fsNetwork},
		{"nfs4", fsNetwork},
		{"cifs", fsNetwork},
		{"smbfs", fsNetwork},
		{"9p", fsNetwork},
		{"webdav", fsNetwork},
		{"afpfs", fsNetwork},
		// Local — must NOT be flagged (no false positives).
		{"apfs", fsLocal},
		{"ext4", fsLocal},
		{"xfs", fsLocal},
		{"btrfs", fsLocal},
		{"tmpfs", fsLocal},
		{"overlay", fsLocal},
		{"zfs", fsLocal},
		// Unknown/empty defaults to LOCAL — refuse only positively-identified bad FS.
		{"", fsLocal},
		{"some-future-local-fs", fsLocal},
		{"  apfs  ", fsLocal}, // trimmed
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.class, classifyFSName(tc.name), "classify %q", tc.name)
		})
	}
}

func TestFSClass_Unsafe(t *testing.T) {
	assert.False(t, fsLocal.unsafe(), "local disk is safe for WAL")
	assert.True(t, fsFuse.unsafe(), "FUSE is unsafe for WAL")
	assert.True(t, fsNetwork.unsafe(), "network FS is unsafe for WAL")
}

func TestLinuxFSMagicName(t *testing.T) {
	// Known-unsafe magics from linux/magic.h -> names the classifier flags.
	unsafe := map[int64]string{
		0x65735546: "fuse",
		0x6969:     "nfs",
		0xff534d42: "cifs",
		0xfe534d42: "smb2",
		0x517b:     "smb",
		0x01021997: "9p",
	}
	for magic, name := range unsafe {
		got := linuxFSMagicName(magic)
		assert.Equal(t, name, got, "magic %#x", magic)
		assert.True(t, classifyFSName(got).unsafe(), "magic %#x (%s) must classify unsafe", magic, got)
	}
	// Local magics classify safe (either mapped to a local name or "" -> local).
	for _, magic := range []int64{0xef53 /*ext*/, 0x58465342 /*xfs*/, 0x9123683e /*btrfs*/, 0x01021994 /*tmpfs*/} {
		assert.False(t, classifyFSName(linuxFSMagicName(magic)).unsafe(), "magic %#x must be safe", magic)
	}
	// Unrecognized magic -> "" -> local (no false positive).
	assert.Equal(t, "", linuxFSMagicName(0x12345678))
	assert.False(t, classifyFSName("").unsafe())
}

// TestWriteRefusedError: the write-refusal error wraps the sentinel, names the
// filesystem, and makes clear no override enables writes (MTIX-58).
func TestWriteRefusedError(t *testing.T) {
	err := writeRefusedError("/x/.mtix/data/mtix.db", "macfuse")
	require.Error(t, err)
	assert.ErrorIs(t, err, errUnsafeFilesystem)
	assert.Contains(t, err.Error(), "macfuse", "names the filesystem type")
	assert.Contains(t, err.Error(), "No environment override", "makes clear there is no write override")
}

// sanity: the errors package is used (keeps import if assertions change)
var _ = errors.Is
