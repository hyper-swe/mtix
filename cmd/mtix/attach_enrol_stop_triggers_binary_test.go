// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func skipIfRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("skipped as root: root ignores mode-000 files and directories")
	}
}

// stopTriggerBoard builds a listed board checkout holding a marker with the
// given content and the given number of aside stores (data absent when one).
func stopTriggerBoard(t *testing.T, marker string, asides int) (root, markerPath string) {
	t.Helper()
	st := testutil.OperatorState(t)
	root = listedBoardCheckout(t, st)
	prepareEnrolGuardCase(t, root, "torn-marker")
	mtix := filepath.Join(root, ".mtix")
	markerPath = filepath.Join(mtix, "enroll.d", "in-progress.json")
	require.NoError(t, os.WriteFile(markerPath, []byte(marker), 0o600))
	if asides == 1 {
		aside := filepath.Join(mtix, "data.pre-enrol-20261108")
		require.NoError(t, os.Rename(filepath.Join(mtix, "data"), aside))
		require.NoError(t, os.WriteFile(filepath.Join(aside, "original.db"), []byte("original"), 0o600))
	}
	return root, markerPath
}

// MTIX-112.17.16.21.2.4 (round 2): trigger 1 on its own. A marker that cannot be
// read (not a decode failure) beside a lock that is absent or free still prints
// STOP: the marker may belong to another user's live enrolment. The prose does
// not name board.enrol-lock, because that row is not registered for these locks.
func TestEnrolGuard_RealBinary_UnreadableMarkerFreeOrAbsentLock_PrintsStop(t *testing.T) {
	skipIfRoot(t)
	binary := buildRecoveryBinary(t)
	for _, lock := range []string{"absent", "released"} {
		for _, asides := range []int{0, 1} {
			root, marker := stopTriggerBoard(t, "{}", asides)
			if lock == "released" {
				prepareEnrolGuardCase(t, root, "released")
			}
			require.NoError(t, os.Chmod(marker, 0o000))
			t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })

			rows := runDoctorRows(t, binary, root)
			row := recoveryRow(rows, "board.enrol-interrupted")
			require.NotNil(t, row, "lock=%s asides=%d", lock, asides)
			assertNoStoreMovingCommand(t, row.Fix)
			assert.NotContains(t, row.Fix, "board.enrol-lock", "that row is not registered here: %s", row.Fix)
			assert.Contains(t, row.Fix, marker, "the prose names the marker to repair")
			assert.Nil(t, recoveryRow(rows, "board.enrol-lock"))
			_, statErr := os.Lstat(marker)
			assert.NoError(t, statErr, "the marker stays")
		}
	}
}

// Trigger 2 on its own: a marker whose content cannot be decoded (readable) beside a
// lock that cannot be probed, with no aside store, prints STOP, not rm.
func TestEnrolGuard_RealBinary_DecodeTornMarkerUnprobeableLock_PrintsStop(t *testing.T) {
	skipIfRoot(t)
	binary := buildRecoveryBinary(t)
	for _, lockKind := range []string{"directory", "mode-000"} {
		root, marker := stopTriggerBoard(t, "{torn", 0)
		lockDir := filepath.Join(root, ".mtix", "enroll.d", "data")
		require.NoError(t, os.MkdirAll(lockDir, 0o700))
		lock := filepath.Join(lockDir, "enroll.lock")
		if lockKind == "directory" {
			require.NoError(t, os.Mkdir(lock, 0o700))
		} else {
			require.NoError(t, os.WriteFile(lock, nil, 0o000))
			t.Cleanup(func() { _ = os.Chmod(lock, 0o600) })
		}

		rows := runDoctorRows(t, binary, root)
		row := recoveryRow(rows, "board.enrol-interrupted")
		require.NotNil(t, row, lockKind)
		assertNoStoreMovingCommand(t, row.Fix)
		assert.Contains(t, row.Fix, "board.enrol-lock", "the lock row is registered here")
		require.NotNil(t, recoveryRow(rows, "board.enrol-lock"), lockKind)
		_, statErr := os.Lstat(marker)
		assert.NoError(t, statErr, "the marker stays")
	}
}

// MTIX-112.17.16.21.2.4 (round 2) F2: when the lock path cannot be inspected the
// printed text is prose naming the directories to repair, and it matches what
// MoveAsideUnprobeableEnrolLock does: it refuses, and --fix moves nothing.
func TestEnrolGuard_RealBinary_LockPathNotInspectable_PrintsRepairProseNotFix(t *testing.T) {
	skipIfRoot(t)
	binary := buildRecoveryBinary(t)
	for _, kind := range []string{"unsearchable", "not-a-directory"} {
		st := testutil.OperatorState(t)
		root := listedBoardCheckout(t, st)
		enrolDir := filepath.Join(root, ".mtix", "enroll.d")
		data := filepath.Join(enrolDir, "data")
		require.NoError(t, os.MkdirAll(enrolDir, 0o700))
		if kind == "unsearchable" {
			require.NoError(t, os.MkdirAll(data, 0o700))
			require.NoError(t, os.Chmod(data, 0o000))
			t.Cleanup(func() { _ = os.Chmod(data, 0o700) })
		} else {
			require.NoError(t, os.WriteFile(data, []byte("x"), 0o600))
		}

		row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-lock")
		require.NotNil(t, row, kind)
		assert.NotContains(t, row.Fix, "mtix doctor --fix", "the printed fix must be able to work: %s", row.Fix)
		assert.Contains(t, row.Fix, data, "the prose names the directory to repair")
		assert.Contains(t, row.Fix, "ask their owner")

		// What the row's fixer accepts: it refuses on the same inspection error.
		require.Error(t, service.MoveAsideUnprobeableEnrolLock(filepath.Join(root, ".mtix")), kind)

		_, _ = runPrintedFix(t, root, binary, "mtix doctor --fix")
		require.NoError(t, os.Chmod(data, 0o700)) // restores the directory so it can be listed
		entries, err := os.ReadDir(enrolDir)
		require.NoError(t, err)
		require.Len(t, entries, 1, "--fix moved nothing: only data remains in %s", enrolDir)
		assert.Equal(t, "data", entries[0].Name())
	}
}
