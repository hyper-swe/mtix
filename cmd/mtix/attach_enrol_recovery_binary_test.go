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
		var exit *exec.ExitError // other doctor FAIL rows may set exit 1; the row itself must clear
		if fixErr != nil {
			require.ErrorAs(t, fixErr, &exit, string(fixOut))
		}
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
	require.NoError(t, os.MkdirAll(aside, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(aside, "original.db"), []byte("original"), 0o600))

	row := recoveryRow(runDoctorRows(t, binary, root), "board.enrol-interrupted")
	require.NotNil(t, row)
	out, err := runPrintedFix(t, root, binary, row.Fix)
	require.NoError(t, err, string(out))

	body, err := os.ReadFile(filepath.Join(root, ".mtix", "data", "original.db"))
	require.NoError(t, err, "the original store must be back in place")
	assert.Equal(t, "original", string(body))
	_, err = os.Stat(filepath.Join(root, ".mtix", "data.kept-aside"))
	assert.NoError(t, err, "the partial store is kept, never deleted")
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
	assert.NotContains(t, row.Fix, "doctor --fix")

	cmd := exec.CommandContext(t.Context(), binary, "doctor", "--fix", "--json")
	cmd.Dir = root
	_ = cmd.Run() // other rows may fail; the lock must stay either way
	_, statErr := os.Lstat(lock)
	assert.NoError(t, statErr, "an unprovable lock is never moved")
	_, statErr = os.Lstat(lock + ".unprobeable")
	assert.True(t, os.IsNotExist(statErr))
}
