// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	"github.com/hyper-swe/mtix/internal/testutil"
)

// An open followed by a close can leave identical database bytes. Observe the
// actual inode's IN_OPEN events synchronously, before any post-call snapshot.
func TestEnrolGuard_ExistingStore_RefusalNeverOpensDatabase(t *testing.T) {
	for _, path := range []string{"drain", "empty-key-drain", "pi-wait", "dispatcher"} {
		t.Run(path, func(t *testing.T) {
			st := testutil.OperatorState(t)
			root := listedBoardCheckout(t, st)
			holdGlueEnrolLock(t, root)
			fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
			require.NoError(t, err)
			defer func() { require.NoError(t, unix.Close(fd)) }()
			_, err = unix.InotifyAddWatch(fd, filepath.Join(root, ".mtix", "data", "mtix.db"), unix.IN_OPEN)
			require.NoError(t, err)
			require.Error(t, invokeSkippedStorePath(t, st, root, path))
			var events [4096]byte
			_, err = unix.Read(fd, events[:])
			assert.ErrorIs(t, err, unix.EAGAIN, "no transient open/close may hide behind unchanged database bytes")
		})
	}
}
