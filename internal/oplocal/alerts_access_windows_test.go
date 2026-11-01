// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package oplocal

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestAlertState_ProtectedACL_RefusesBeforePrepare(t *testing.T) {
	for _, name := range []string{"alerts.jsonl", "alerts.1.jsonl", "alerts.2.jsonl", "alerts-pending.json", "alerts.lock", "rate"} {
		t.Run(name, func(t *testing.T) {
			s := alertFixture(t)
			row, err := s.RecordAlert(t.Context(), alertInput(), alertTime())
			require.NoError(t, err)
			target := name
			if name == "rate" {
				target = alertRateName(row.Agent, row.Class)
			}
			dir, err := s.directory(false)
			require.NoError(t, err)
			if _, readErr := readData(dir, target); errors.Is(readErr, fs.ErrNotExist) {
				require.NoError(t, writeData(dir, target, []byte{}, false, nil, nil))
			}
			require.NoError(t, closeFile(dir))
			path, err := s.Path()
			require.NoError(t, err)
			before, err := os.ReadFile(filepath.Join(path, "alerts-pending.json"))
			require.NoError(t, err)
			descriptor, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
			require.NoError(t, err)
			acl, _, err := descriptor.DACL()
			require.NoError(t, err)
			require.NoError(t, windows.SetNamedSecurityInfo(filepath.Join(path, target), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
			_, err = s.RecordAlert(t.Context(), alertInput(), alertTime())
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
			after, err := os.ReadFile(filepath.Join(path, "alerts-pending.json"))
			require.NoError(t, err)
			require.Equal(t, before, after)
			_, err = s.RecentAlerts(t.Context(), 0)
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		})
	}
}
