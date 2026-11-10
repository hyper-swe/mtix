// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

func doctorRow(t *testing.T, mtixDir, id string) *service.DoctorResult {
	t.Helper()
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, registerEnrolDoctorChecks(svc, mtixDir))
	rows, _ := svc.Run(context.Background(), service.DoctorRunOptions{})
	for i := range rows {
		if rows[i].ID == id {
			return &rows[i]
		}
	}
	return nil
}

func writeAcceptRecord(t *testing.T, mtixDir, aside string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(mtixDir, "enroll.d"), 0o700))
	require.NoError(t, os.WriteFile(service.HubAcceptedPath(mtixDir),
		[]byte(`{"report":"/r.json","aside_dir":"`+aside+`","backup_path":"/b.db","accepted_at":"2026-11-05T00:00:00Z"}`), 0o600))
}

// TestRegisterEnrolDoctorChecks_AcceptHubRecord_ProducesTheWarningRow goes
// through the production registration the doctor command calls, so dropping the
// accept-hub row from it fails here and not only in a direct call of the row.
func TestRegisterEnrolDoctorChecks_AcceptHubRecord_ProducesTheWarningRow(t *testing.T) {
	mtixDir := filepath.Join(t.TempDir(), ".mtix")
	aside := filepath.Join(mtixDir, "data.pre-enrol-T")
	require.NoError(t, os.MkdirAll(aside, 0o700))
	writeAcceptRecord(t, mtixDir, aside)

	row := doctorRow(t, mtixDir, "board.enrol-hub-accepted")

	require.NotNil(t, row)
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Contains(t, row.Fix, "hub-accepted.json")
	assert.Contains(t, row.Detail, aside)
}

// TestRegisterEnrolDoctorChecks_LostLocalRecord_WarnsAndNamesTheReport: a record
// of a locally written value that lost to the hub (MTIX-112.17.16.18) is a
// warning row that names the lost-value report, the old store and the backup.
func TestRegisterEnrolDoctorChecks_LostLocalRecord_WarnsAndNamesTheReport(t *testing.T) {
	mtixDir := filepath.Join(t.TempDir(), ".mtix")
	aside := filepath.Join(mtixDir, "data.pre-enrol-T")
	require.NoError(t, os.MkdirAll(aside, 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(mtixDir, "enroll.d"), 0o700))
	require.NoError(t, os.WriteFile(service.HubAcceptedPath(mtixDir),
		[]byte(`{"report":"/lost.json","aside_dir":"`+aside+`","backup_path":"/b.db","accepted_at":"2026-11-05T00:00:00Z","lost_local_report":"/lost.json"}`), 0o600))

	row := doctorRow(t, mtixDir, "board.enrol-hub-accepted")

	require.NotNil(t, row)
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Contains(t, row.Detail, "/lost.json")
	assert.Contains(t, row.Detail, "value written here lost")
	assert.NotContains(t, row.Detail, "differed from it", "only the lost-value report exists")
	assert.Contains(t, row.Detail, aside)
	assert.Equal(t, "/lost.json", row.Data["lost_local_report"])
}

// TestRegisterEnrolDoctorChecks_StaleAcceptRecord_WarnsItIsStale: the record names
// an old store that no longer exists (a recovery put the original back, or it
// was cleared); the row says so and the fix removes the record.
func TestRegisterEnrolDoctorChecks_StaleAcceptRecord_WarnsItIsStale(t *testing.T) {
	mtixDir := filepath.Join(t.TempDir(), ".mtix")
	writeAcceptRecord(t, mtixDir, filepath.Join(mtixDir, "data.pre-enrol-gone"))

	row := doctorRow(t, mtixDir, "board.enrol-hub-accepted")

	require.NotNil(t, row)
	assert.Equal(t, service.DoctorWarn, row.Status)
	assert.Contains(t, row.Detail, "stale")
	assert.Contains(t, row.Fix, "hub-accepted.json")
}

// TestRegisterEnrolDoctorChecks_TornAcceptRecord_IsARowNotACrash: a record that
// cannot be decoded must not take the whole doctor down; it is one failing row
// with the exact fix.
func TestRegisterEnrolDoctorChecks_TornAcceptRecord_IsARowNotACrash(t *testing.T) {
	mtixDir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(mtixDir, "enroll.d"), 0o700))
	require.NoError(t, os.WriteFile(service.HubAcceptedPath(mtixDir), []byte("{torn"), 0o600))
	svc := service.NewDoctorService(service.DoctorOptions{})

	require.NoError(t, registerEnrolDoctorChecks(svc, mtixDir))

	row := doctorRow(t, mtixDir, "board.enrol-hub-accepted")
	require.NotNil(t, row)
	assert.Equal(t, service.DoctorFail, row.Status)
	assert.Contains(t, row.Fix, "hub-accepted.json")
}

// TestDoctor_EnrolInterrupted_FixTakenFromTheDoctorOutput_RestoresAndClearsTheAcceptRecord
// takes the recovery from what the doctor prints, through the production
// registration, and runs it through sh: the original store is back, the marker
// and the stale accept-hub record are gone. (A torn 8-byte store is an open
// question: doctor opens the store first and prints no fix for it.)
func TestDoctor_EnrolInterrupted_FixTakenFromTheDoctorOutput_RestoresAndClearsTheAcceptRecord(t *testing.T) {
	w := newRecoveryWorld(t, "aside", "data")
	writeAcceptRecord(t, w.mtix, w.aside)

	row := doctorRow(t, w.mtix, "board.enrol-interrupted")

	require.NotNil(t, row)
	require.Equal(t, service.DoctorFail, row.Status)
	out, err := exec.Command("sh", "-c", row.Fix).CombinedOutput() //nolint:gosec // G204: the fix text is the unit under test, built from a temp dir
	require.NoError(t, err, "%s\n%s", row.Fix, out)
	assert.Equal(t, "ORIGINAL", w.read(t, filepath.Join(w.data, "mtix.db")))
	assert.False(t, w.exists(w.marker))
	assert.False(t, w.exists(service.HubAcceptedPath(w.mtix)), "the stale accept-hub record goes with the recovery")
}

// TestDoctor_EnrolInterrupted_NoDataDirectory_FixClearsTheAcceptRecord: the same
// for a crash right after the move, when there is no data directory to keep.
func TestDoctor_EnrolInterrupted_NoDataDirectory_FixClearsTheAcceptRecord(t *testing.T) {
	w := newRecoveryWorld(t, "aside", "")
	writeAcceptRecord(t, w.mtix, w.aside)

	row := doctorRow(t, w.mtix, "board.enrol-interrupted")

	require.NotNil(t, row)
	out, err := exec.Command("sh", "-c", row.Fix).CombinedOutput() //nolint:gosec // G204: the fix text is the unit under test, built from a temp dir
	require.NoError(t, err, "%s\n%s", row.Fix, out)
	assert.Equal(t, "ORIGINAL", w.read(t, filepath.Join(w.data, "mtix.db")))
	assert.False(t, w.exists(service.HubAcceptedPath(w.mtix)))
}

func TestEnrolInterruptedFix_NoAcceptRecord_StillRuns(t *testing.T) {
	w := newRecoveryWorld(t, "aside", "")
	row := doctorRow(t, w.mtix, "board.enrol-interrupted")
	require.NotNil(t, row)
	out, err := exec.Command("sh", "-c", row.Fix).CombinedOutput() //nolint:gosec // G204: the fix text is the unit under test, built from a temp dir
	require.NoError(t, err, "%s\n%s", row.Fix, out)
	assert.Equal(t, "ORIGINAL", w.read(t, filepath.Join(w.data, "mtix.db")))
}

// TestDoctor_EnrolInterrupted_DetailSaysTheCommandHoldsOnlyUntilDataChanges: for
// each printed-command branch the detail says the command was chosen for the
// data directory as it is now, while the Fix text and the row's data keys stay
// exactly as before (wi-28).
func TestDoctor_EnrolInterrupted_DetailSaysTheCommandHoldsOnlyUntilDataChanges(t *testing.T) {
	q := func(s string) string { return "'" + s + "'" } // temp paths hold no quote
	restore := func(w *recoveryWorld) string {
		return "mv " + q(w.data) + " " + q(w.data+".kept-aside") + " && mv " + q(w.aside) + " " + q(w.data) +
			" && rm " + q(w.marker) + " && rm -f " + q(service.HubAcceptedPath(w.mtix))
	}
	cases := []struct {
		name                  string
		originalIn, partialIn string
		emptyData             bool
		wantFix               func(w *recoveryWorld) string
	}{
		{"data absent", "aside", "", false, func(w *recoveryWorld) string {
			return "mv " + q(w.aside) + " " + q(w.data) + " && rm " + q(w.marker) + " && rm -f " + q(service.HubAcceptedPath(w.mtix))
		}},
		{"data empty", "aside", "", true, restore},
		{"data live", "aside", "data", false, restore},
		{"store never moved", "data", "", false, func(w *recoveryWorld) string { return "rm " + q(w.marker) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRecoveryWorld(t, tc.originalIn, tc.partialIn)
			if tc.emptyData {
				require.NoError(t, os.MkdirAll(w.data, 0o700))
			}

			row := doctorRow(t, w.mtix, "board.enrol-interrupted")

			require.NotNil(t, row)
			assert.Equal(t, service.DoctorFail, row.Status)
			assert.Contains(t, row.Detail, "; the printed command was chosen for the data directory as it is now and is valid only until that directory changes; run mtix doctor again after any change to it")
			assert.Equal(t, tc.wantFix(w), row.Fix, "the Fix text is unchanged")
			keys := make([]string, 0, len(row.Data))
			for k := range row.Data {
				keys = append(keys, k)
			}
			assert.ElementsMatch(t, []string{"aside_dir", "backup_path", "started_at", "pid"}, keys)
		})
	}
}

// TestDoctor_EnrolInterrupted_TimeBoundSentenceOnlyWithACommand: a readable
// marker whose fix is STOP prose (the enrolment lock is held) gets no time-bound
// sentence, and a torn marker gets it exactly when its fix is a command (wi-28).
func TestDoctor_EnrolInterrupted_TimeBoundSentenceOnlyWithACommand(t *testing.T) {
	tear := func(t *testing.T, w *recoveryWorld) {
		t.Helper()
		require.NoError(t, os.WriteFile(w.marker, []byte("{torn"), 0o600))
	}
	cases := []struct {
		name        string
		prepare     func(t *testing.T, w *recoveryWorld)
		wantSTOP    bool
		wantCommand string // prefix of the Fix when it is a command
	}{
		{"lock held prints STOP prose", func(t *testing.T, w *recoveryWorld) {
			lock, err := pushlock.AcquireNamed(filepath.Join(w.mtix, "enroll.d"), "enroll.lock")
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, lock.Release()) })
		}, true, ""},
		{"torn marker, one aside, data absent", tear, false, "mv "},
		{"torn marker, several asides", func(t *testing.T, w *recoveryWorld) {
			tear(t, w)
			require.NoError(t, os.MkdirAll(filepath.Join(w.mtix, "data.pre-enrol-20261106T010203Z"), 0o700))
		}, true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRecoveryWorld(t, "aside", "")
			tc.prepare(t, w)

			row := doctorRow(t, w.mtix, "board.enrol-interrupted")

			require.NotNil(t, row)
			assert.Equal(t, service.DoctorFail, row.Status)
			if tc.wantSTOP {
				assert.True(t, strings.HasPrefix(row.Fix, "STOP:"), row.Fix)
				assert.NotContains(t, row.Detail, enrolFixTimeBound, "STOP prose prints no command")
				return
			}
			assert.True(t, strings.HasPrefix(row.Fix, tc.wantCommand), row.Fix)
			assert.Contains(t, row.Detail, "; "+enrolFixTimeBound)
		})
	}
}
