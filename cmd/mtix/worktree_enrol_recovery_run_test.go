// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// recoveryWorld is the on-disk state a crashed enrolment leaves: the marker,
// and the original store and a half-built one in the places the crash point
// dictates (MTIX-112.17.16.7).
type recoveryWorld struct {
	mtix, data, aside, marker string
}

func newRecoveryWorld(t *testing.T, originalIn, partialIn string) *recoveryWorld {
	t.Helper()
	return newRecoveryWorldIn(t, "wt", originalIn, partialIn)
}

// newRecoveryWorldIn is newRecoveryWorld with the worktree directory named dirName.
func newRecoveryWorldIn(t *testing.T, dirName, originalIn, partialIn string) *recoveryWorld {
	t.Helper()
	mtix := filepath.Join(t.TempDir(), dirName, ".mtix")
	w := &recoveryWorld{mtix: mtix, data: filepath.Join(mtix, "data"),
		aside:  filepath.Join(mtix, "data.pre-enrol-20261105T010203Z"),
		marker: filepath.Join(mtix, "enroll.d", "in-progress.json")}
	require.NoError(t, os.MkdirAll(mtix, 0o700))
	place := func(dir, name, content string) {
		if dir == "" {
			return
		}
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600))
	}
	place(map[string]string{"data": w.data, "aside": w.aside}[originalIn], "mtix.db", "ORIGINAL")
	place(map[string]string{"data": w.data}[partialIn], "mtix.db", "PARTIAL")
	require.NoError(t, service.WriteEnrolMarker(mtix, service.EnrolMarker{Aside: w.aside, Backup: filepath.Join(mtix, "backups", "bk.db"), Started: "t", PID: 9}))
	return w
}

func (w *recoveryWorld) read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: a test tree
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

func (w *recoveryWorld) exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// runFix reads the marker as doctor does and runs the printed fix through sh.
func (w *recoveryWorld) runFix(t *testing.T) string {
	t.Helper()
	marker, err := service.ReadEnrolMarker(w.mtix)
	require.NoError(t, err)
	require.NotNil(t, marker)
	fix := enrolInterruptedFix(w.mtix, marker)
	out, runErr := exec.Command("sh", "-c", fix).CombinedOutput() //nolint:gosec // G204: the fix text is the unit under test, built from a temp dir
	require.NoError(t, runErr, "the printed recovery must run: %s\n%s", fix, out)
	return fix
}

// TestDoctor_EnrolInterrupted_PrintedFixRestoresTheBoardAtEveryCrashPoint runs
// the recovery text through sh for each place the enrolment can die. After it,
// data holds the original store, a half-built store is kept (never deleted)
// and the marker is gone. Swapping the two moves, keeping the marker and
// deleting the partial store each fail one of these assertions.
func TestDoctor_EnrolInterrupted_PrintedFixRestoresTheBoardAtEveryCrashPoint(t *testing.T) {
	cases := []struct {
		name                  string
		originalIn, partialIn string
		wantKept              bool
	}{
		{"crash after the marker, before the move", "data", "", false},
		{"crash right after the move, no data directory", "aside", "", false},
		{"crash during the clone, partial store in data", "aside", "data", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newRecoveryWorld(t, tc.originalIn, tc.partialIn)

			w.runFix(t)

			assert.Equal(t, "ORIGINAL", w.read(t, filepath.Join(w.data, "mtix.db")), "data holds the original store")
			assert.False(t, w.exists(w.aside), "the aside directory is gone: its content went back to data")
			assert.False(t, w.exists(w.marker), "the marker is removed")
			kept := filepath.Join(w.mtix, "data.kept-aside")
			if tc.wantKept {
				assert.Equal(t, "PARTIAL", w.read(t, filepath.Join(kept, "mtix.db")), "the partial store is kept, not deleted")
				return
			}
			assert.False(t, w.exists(kept), "nothing is kept aside when no partial store existed")
		})
	}
}

// TestDoctor_EnrolInterrupted_PrintedFixIsShellSafe runs the recovery for
// worktree paths an agent may meet (branch names become directory names): a
// dollar sign, a backtick command and a single quote. The path must reach mv
// and rm as one literal word and nothing inside it may run.
func TestDoctor_EnrolInterrupted_PrintedFixIsShellSafe(t *testing.T) {
	names := []string{"cost $HOME", "tick `touch CANARY-BACKTICK`", "sub $(touch CANARY-SUBST)", "it's", `dq "x"`}
	crashes := []struct{ name, originalIn, partialIn string }{
		{"before the move", "data", ""},
		{"right after the move", "aside", ""},
		{"during the clone", "aside", "data"},
	}
	for _, dirName := range names {
		for _, c := range crashes {
			t.Run(dirName+"/"+c.name, func(t *testing.T) {
				w := newRecoveryWorldIn(t, dirName, c.originalIn, c.partialIn)

				fix := w.runFix(t)

				assert.Equal(t, "ORIGINAL", w.read(t, filepath.Join(w.data, "mtix.db")), fix)
				assert.False(t, w.exists(w.marker), "the marker is removed")
				for _, canary := range []string{"CANARY-BACKTICK", "CANARY-SUBST"} {
					assert.False(t, w.exists(filepath.Join(filepath.Dir(w.mtix), canary)), "nothing in the path ran: "+canary)
				}
			})
		}
	}
}
