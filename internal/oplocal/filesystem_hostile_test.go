// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests operator record and alert filenames independently of hostile content.
package oplocal_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

func TestHostileIDs_NeverCreateFilesOutsideBaseDir(t *testing.T) {
	testHostileOperatorFiles(t, false)
}
func TestHostileIDs_NeverCreateSurpriseFilenames(t *testing.T) { testHostileOperatorFiles(t, true) }

var validRecord = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func testHostileOperatorFiles(t *testing.T, names bool) {
	t.Helper()
	for _, f := range append(testutil.HostileIDs(), testutil.HostileText()...) {
		t.Run(f.Name, func(t *testing.T) {
			layout := hostguard.Isolate(t)
			s := oplocal.New(oplocal.Env{GOOS: runtime.GOOS, Home: layout.Home, Values: map[string]string{"XDG_CONFIG_HOME": layout.Config, "APPDATA": layout.Config}})
			require.NoError(t, s.WriteJSON("inbox", map[string]string{"event_id": f.Value, "title": f.Value}))
			// A hostile value as the caller-supplied record name is refused unless it
			// is itself a plain record name; it never reaches the directory otherwise.
			recordOK := validRecord.MatchString(f.Value) && f.Value != "host-id"
			recordErr := s.WriteJSON(f.Value, map[string]string{"event_id": f.Value})
			created, createErr := s.CreateJSON(f.Value, map[string]string{"event_id": f.Value})
			if recordOK {
				require.NoError(t, recordErr)
				require.NoError(t, createErr)
				require.False(t, created, "existing record is kept")
			} else {
				require.Error(t, recordErr)
				require.Error(t, createErr)
				require.False(t, created)
			}
			input := oplocal.Alert{ID: f.Value, Agent: "test-agent", Class: oplocal.AlertClassWakeBackoff, Detail: f.Value}
			row, err := s.RecordAlert(t.Context(), input, time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC))
			if oplocal.ValidateAlertInput(input) != nil {
				require.ErrorIs(t, err, model.ErrInvalidInput)
			} else {
				require.NoError(t, err)
				require.Regexp(t, `^[0-9a-f]{32}$`, row.ID)
			}
			base, err := s.Path()
			require.NoError(t, err)
			require.FileExists(t, filepath.Join(base, "inbox.json"))
			require.NoError(t, filepath.WalkDir(layout.Root, func(path string, e fs.DirEntry, walkErr error) error {
				require.NoError(t, walkErr)
				if e.IsDir() {
					return nil
				}
				require.Equal(t, base, filepath.Dir(path), "outside operator directory")
				require.Zero(t, e.Type()&os.ModeSymlink)
				if !names {
					return nil
				}
				require.Regexp(t, `^(host-id|inbox\.json|[a-z][a-z0-9_-]{0,63}\.json|alerts(?:-pending\.json|\.jsonl|\.lock)|alert-rate-[0-9a-f]{32}\.json)$`, e.Name())
				return nil
			}))
		})
	}
}
