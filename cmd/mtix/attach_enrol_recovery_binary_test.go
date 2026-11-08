// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil"
)

type recoveryDoctorRow struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

// runDoctorRows runs the real binary's top-level doctor and decodes its rows.
// Other FAIL rows may set exit 1; a missing or unparsable table is the defect.
func runDoctorRows(t *testing.T, binary, root string) []recoveryDoctorRow {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, "doctor", "--json")
	cmd.Dir = root
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit, stderr.String())
	}
	var rows []recoveryDoctorRow
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rows), "doctor printed no rows; stdout %q stderr %q", stdout.String(), stderr.String())
	return rows
}

func recoveryRow(rows []recoveryDoctorRow, id string) *recoveryDoctorRow {
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}

// MTIX-112.17.16.21.2: an attach.enrol-blocked refusal for a torn marker or an
// unprobeable lock names a fix that works when run. Doctor must print a FAIL row
// with an exact command, and running that command must clear the refusal.
func TestEnrolGuard_RealBinary_TornMarkerAndProbeError_PrintedFixWorks(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "mtix")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput()
	require.NoError(t, err, "build real binary: %s", out)
	for _, tt := range []struct{ mode, row string }{
		{"torn-marker", "board.enrol-interrupted"},
		{"probe-error", "board.enrol-lock"},
	} {
		t.Run(tt.mode, func(t *testing.T) { recoveryCase(t, binary, tt.mode, tt.row) })
	}
}

func recoveryCase(t *testing.T, binary, mode, rowID string) {
	t.Helper()
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	prepareEnrolGuardCase(t, root, mode)

	attach := exec.CommandContext(t.Context(), binary, "attach", "session-start", "--harness", "claude-code", "--protocol", "1")
	attach.Dir = root
	require.NoError(t, attach.Run())
	found := false
	for _, alert := range attachAlerts(t, st) {
		if alert.Class == "attach.enrol-blocked" {
			found = true
			assert.Contains(t, alert.Fix, "mtix doctor")
			assert.Contains(t, alert.Fix, rowID, "the alert must name the doctor row that prints the exact fix")
		}
	}
	require.True(t, found, "the refusal must be recorded as attach.enrol-blocked")

	row := recoveryRow(runDoctorRows(t, binary, root), rowID)
	require.NotNil(t, row, "doctor must report %s", rowID)
	assert.True(t, strings.EqualFold(row.Status, "fail"), row.Status)
	require.NotEmpty(t, row.Fix)

	fixOut, fixErr := runPrintedFix(t, root, binary, row.Fix)
	if strings.HasPrefix(row.Fix, "mtix doctor") {
		// The repaired row must show pass in the fix run itself; other rows may fail the exit code.
		assert.NotContains(t, string(fixOut), "[FAIL] "+rowID, "the fix run must not still print the repaired row as FAIL")
	} else {
		require.NoError(t, fixErr, "the printed fix must work when run: %s", fixOut)
	}

	assert.Nil(t, recoveryRow(runDoctorRows(t, binary, root), rowID), "the row must clear once its fix has run")
}

func buildRecoveryBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "mtix")
	out, err := exec.CommandContext(t.Context(), "go", "build", "-o", binary, ".").CombinedOutput()
	require.NoError(t, err, "build real binary: %s", out)
	return binary
}

// runPrintedFix runs a printed fix as an agent would; "mtix" names the binary under test.
func runPrintedFix(t *testing.T, root, binary, fix string) ([]byte, error) {
	t.Helper()
	if rest, ok := strings.CutPrefix(fix, "mtix "); ok {
		fix = shellQuote(binary) + " " + rest
	}
	cmd := exec.CommandContext(t.Context(), "sh", "-c", fix)
	cmd.Dir = root
	return cmd.CombinedOutput()
}

// A torn marker with exactly one aside store: the fix puts the original store
// back and keeps the partial one; nothing is lost.
func TestEnrolGuard_RealBinary_TornMarkerOneAside_FixRestoresOriginalStore(t *testing.T) {
	binary := buildRecoveryBinary(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	prepareEnrolGuardCase(t, root, "torn-marker")
	aside := filepath.Join(root, ".mtix", "data.pre-enrol-20261108")
	// A crash right after the move: the live data directory is gone.
	require.NoError(t, os.Rename(filepath.Join(root, ".mtix", "data"), aside))
	require.NoError(t, os.WriteFile(filepath.Join(aside, "original.db"), []byte("original"), 0o600))

	row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-interrupted")
	require.NotNil(t, row)
	_, statErr := os.Lstat(filepath.Join(root, ".mtix", "data"))
	assert.True(t, os.IsNotExist(statErr), "doctor must not create or open a store while the marker is torn")
	out, err := runPrintedFix(t, root, binary, row.Fix)
	require.NoError(t, err, string(out))

	body, err := os.ReadFile(filepath.Join(root, ".mtix", "data", "original.db"))
	require.NoError(t, err, "the original store must be back in place")
	assert.Equal(t, "original", string(body))
	assert.Nil(t, recoveryRow(runDoctorRows(t, binary, root), "board.enrol-interrupted"))
}

// Several aside stores after a torn marker: no store can be picked safely, so
// the row tells the project maintainer to choose, prints no mv or rm, and moves nothing.
func TestEnrolGuard_RealBinary_TornMarkerSeveralAsides_NeverAutoPicks(t *testing.T) {
	binary := buildRecoveryBinary(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	prepareEnrolGuardCase(t, root, "torn-marker")
	mtix := filepath.Join(root, ".mtix")
	asides := []string{filepath.Join(mtix, "data.pre-enrol-a"), filepath.Join(mtix, "data.pre-enrol-b")}
	for _, a := range asides {
		require.NoError(t, os.MkdirAll(a, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(a, "store.db"), []byte(filepath.Base(a)), 0o600))
	}
	row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-interrupted")
	require.NotNil(t, row)
	assert.True(t, strings.HasPrefix(row.Fix, "STOP:"), row.Fix)
	for _, a := range asides {
		assert.Contains(t, row.Fix, a, "the row names every candidate")
	}
	assert.NotContains(t, row.Fix, "mv ")
	assert.NotContains(t, row.Fix, "rm ")

	_, runErr := runPrintedFix(t, root, binary, row.Fix) // prose is not a command: it must fail and change nothing
	assert.Error(t, runErr)
	for _, a := range asides {
		_, err := os.Stat(filepath.Join(a, "store.db"))
		assert.NoError(t, err, "no aside store may be moved or removed")
	}
	_, err := os.Stat(filepath.Join(mtix, "enroll.d", "in-progress.json"))
	assert.NoError(t, err, "the marker stays until the project maintainer decides")
}

// A live enrolment holds the lock with the same primitive the guard uses. Doctor
// printed the row while the path could not be probed; the lock is then replaced
// by a real held lock before the fix runs. The fix must refuse and change nothing.
func TestEnrolGuard_RealBinary_LockFixRefusesWhileAnEnrolmentHoldsIt(t *testing.T) {
	binary := buildRecoveryBinary(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	prepareEnrolGuardCase(t, root, "probe-error")
	row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-lock")
	require.NotNil(t, row)

	lock := filepath.Join(root, ".mtix", "enroll.d", "data", "enroll.lock")
	require.NoError(t, os.Remove(lock))
	heldEnrolTestLock(t, root)
	before := enrolDataSnapshot(t, root)

	out, err := runPrintedFix(t, root, binary, row.Fix)
	require.Error(t, err, "the fix must refuse while an enrolment holds the lock: %s", out)
	info, statErr := os.Lstat(lock)
	require.NoError(t, statErr, "the held lock must not be moved or removed")
	assert.True(t, info.Mode().IsRegular())
	_, statErr = os.Lstat(lock + ".unprobeable")
	assert.True(t, os.IsNotExist(statErr), "nothing may be moved aside")
	assert.Equal(t, before, enrolDataSnapshot(t, root))
}

// A regular lock that cannot be probed (mode 000) cannot be proven unheld: the
// row asks the project maintainer, and doctor --fix refuses and leaves the file where it is.
func TestEnrolGuard_RealBinary_UnprobeableRegularLock_NeverMovedByFix(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipped as root: a root process can open a mode-000 lock, so it never becomes unprobeable")
	}
	binary := buildRecoveryBinary(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	lockDir := filepath.Join(root, ".mtix", "enroll.d", "data")
	require.NoError(t, os.MkdirAll(lockDir, 0o700))
	lock := filepath.Join(lockDir, "enroll.lock")
	require.NoError(t, os.WriteFile(lock, nil, 0o600))
	require.NoError(t, os.Chmod(lock, 0o000))
	t.Cleanup(func() { _ = os.Chmod(lock, 0o600) })

	row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-lock")
	require.NotNil(t, row, "an unprobeable lock must still get a doctor row")
	assert.Contains(t, row.Fix, "project maintainer")
	assert.Contains(t, row.Fix, lockDir+" a real directory", "the prose must name the lock directory, whose owner or link can also fail the probe")
	assert.NotContains(t, row.Fix, "doctor --fix")

	cmd := exec.CommandContext(t.Context(), binary, "doctor", "--fix", "--json")
	cmd.Dir = root
	_ = cmd.Run() // other rows may fail; the lock must stay either way
	_, statErr := os.Lstat(lock)
	assert.NoError(t, statErr, "an unprovable lock is never moved")
	_, statErr = os.Lstat(lock + ".unprobeable")
	assert.True(t, os.IsNotExist(statErr))
}

// F1: after a torn marker, one aside directory is this enrolment's original only
// when the live store is gone. A successful enrolment leaves its aside behind, so
// with a live store the single aside may be a stale old store: the row must print
// the STOP prose, no command, and move nothing.
func TestEnrolGuard_RealBinary_TornMarkerOneStaleAsideLiveStore_NeverAutoRestores(t *testing.T) {
	binary := buildRecoveryBinary(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st) // has a live store
	prepareEnrolGuardCase(t, root, "torn-marker")
	mtix := filepath.Join(root, ".mtix")
	aside := filepath.Join(mtix, "data.pre-enrol-20260101T000000Z")
	require.NoError(t, os.MkdirAll(aside, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(aside, "stale.db"), []byte("stale"), 0o600))
	before := enrolDataSnapshot(t, root)

	row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-interrupted")
	require.NotNil(t, row)
	assert.True(t, strings.HasPrefix(row.Fix, "STOP:"), row.Fix)
	assert.Contains(t, row.Fix, aside)
	assert.NotContains(t, row.Fix, "mv ")
	assert.NotContains(t, row.Fix, "rm ")
	assert.Equal(t, before, enrolDataSnapshot(t, root), "doctor must change nothing")
}

// F2: an unprobeable lock may be held by another user's enrolment. Doctor prints
// its rows without creating or opening a store in the data directory.
func TestEnrolGuard_RealBinary_UnprobeableLock_DoctorDoesNotCreateStore(t *testing.T) {
	binary := buildRecoveryBinary(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	prepareEnrolGuardCase(t, root, "probe-error")
	data := filepath.Join(root, ".mtix", "data")
	moved := filepath.Join(root, ".mtix", "data.pre-enrol-x") // a live enrolment moved the store away
	require.NoError(t, os.Rename(data, moved))

	row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-lock")
	require.NotNil(t, row, "the row must still print")
	_, statErr := os.Lstat(data)
	assert.True(t, os.IsNotExist(statErr), "doctor must not create the data directory past an unprobeable lock")
}

// F3: after a successful fix the same run shows the lock row clear.
func TestEnrolGuard_RealBinary_LockFix_RowShowsPassInTheSameRun(t *testing.T) {
	binary := buildRecoveryBinary(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	prepareEnrolGuardCase(t, root, "probe-error")

	cmd := exec.CommandContext(t.Context(), binary, "doctor", "--fix", "--json")
	cmd.Dir = root
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	_ = cmd.Run() // other rows may fail; the lock row must not
	var rows []recoveryDoctorRow
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &rows), stdout.String())
	row := recoveryRow(rows, "board.enrol-lock")
	require.NotNil(t, row)
	assert.Equal(t, "pass", strings.ToLower(row.Status), row.Detail)
	_, statErr := os.Lstat(filepath.Join(root, ".mtix", "enroll.d", "data", "enroll.lock.unprobeable"))
	assert.NoError(t, statErr, "the lock object was moved aside")
}

func assertNoStoreMovingCommand(t *testing.T, fix string) {
	t.Helper()
	assert.True(t, strings.HasPrefix(fix, "STOP:"), fix)
	assert.Contains(t, fix, "board.enrol-lock")
	assert.NotContains(t, fix, "mv ")
	assert.NotContains(t, fix, "rm ")
}

// MTIX-112.17.16.21.2.4: another user's live enrolment looks like a 0600 marker
// the current user cannot read plus a lock it cannot open. With one aside and an
// absent data directory the old row printed mv; it must print STOP prose and
// nothing may move.
func TestEnrolGuard_RealBinary_UnreadableMarkerAndLock_PrintsNoCommand(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("skipped as root: root can read a mode-000 marker and open a mode-000 lock, so neither becomes unreadable")
	}
	for _, asides := range []int{0, 1} {
		binary := buildRecoveryBinary(t)
		st := testutil.OperatorState(t)
		root := listedBoardCheckout(t, st)
		prepareEnrolGuardCase(t, root, "torn-marker")
		mtix := filepath.Join(root, ".mtix")
		marker := filepath.Join(mtix, "enroll.d", "in-progress.json")
		require.NoError(t, os.Chmod(marker, 0o000))
		t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })
		lockDir := filepath.Join(mtix, "enroll.d", "data")
		require.NoError(t, os.MkdirAll(lockDir, 0o700))
		lock := filepath.Join(lockDir, "enroll.lock")
		require.NoError(t, os.WriteFile(lock, nil, 0o600))
		require.NoError(t, os.Chmod(lock, 0o000))
		t.Cleanup(func() { _ = os.Chmod(lock, 0o600) })
		aside := filepath.Join(mtix, "data.pre-enrol-20261108")
		if asides == 1 {
			require.NoError(t, os.Rename(filepath.Join(mtix, "data"), aside))
			require.NoError(t, os.WriteFile(filepath.Join(aside, "original.db"), []byte("original"), 0o600))
		}
		var before map[string]enrolDataEntry
		if asides == 0 {
			before = enrolDataSnapshot(t, root) // the data directory exists only in this case
		}

		row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-interrupted")
		require.NotNil(t, row, "asides=%d", asides)
		assertNoStoreMovingCommand(t, row.Fix)
		if asides == 0 {
			assert.Equal(t, before, enrolDataSnapshot(t, root), "nothing may move")
		}
		_, statErr := os.Lstat(marker)
		assert.NoError(t, statErr, "the marker stays")
		_, statErr = os.Lstat(filepath.Join(mtix, "data"))
		assert.Equal(t, asides == 0, statErr == nil, "doctor must not create or move a data directory")
		if asides == 1 {
			_, statErr = os.Lstat(filepath.Join(aside, "original.db"))
			assert.NoError(t, statErr, "the aside store stays where it is")
		}
	}
}

// A marker that decodes badly but whose lock is free and probeable keeps its
// runnable fix (regression for the narrowed rule above).
func TestEnrolGuard_RealBinary_DecodeTornMarkerFreeLock_KeepsRunnableFix(t *testing.T) {
	binary := buildRecoveryBinary(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	prepareEnrolGuardCase(t, root, "torn-marker")
	prepareEnrolGuardCase(t, root, "released") // a lock file that exists and is free

	row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-interrupted")
	require.NotNil(t, row)
	assert.True(t, strings.HasPrefix(row.Fix, "rm "), row.Fix)
	out, err := runPrintedFix(t, root, binary, row.Fix)
	require.NoError(t, err, string(out))
	assert.Nil(t, recoveryRow(runDoctorRows(t, binary, root), "board.enrol-interrupted"))
}

// A valid marker with an unprobeable lock may be a live enrolment too: the
// valid-marker row prints STOP prose, not mv/rm.
func TestEnrolGuard_RealBinary_ValidMarkerUnprobeableLock_PrintsNoCommand(t *testing.T) {
	binary := buildRecoveryBinary(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	prepareEnrolGuardCase(t, root, "interrupted")
	prepareEnrolGuardCase(t, root, "probe-error")
	before := enrolDataSnapshot(t, root)

	row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-interrupted")
	require.NotNil(t, row)
	assertNoStoreMovingCommand(t, row.Fix)
	assert.Equal(t, before, enrolDataSnapshot(t, root))
}
