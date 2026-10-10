// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package oplocal

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
	"path/filepath"
	"testing"
)

func TestPlatformState_ValidInput(t *testing.T) {
	root := t.TempDir()
	s := New(Env{GOOS: "windows", Home: `C:\Users\test`, Root: root, Values: map[string]string{"APPDATA": `C:\Users\test\AppData\Roaming`, "TMPDIR": `C:\Temp`}})
	require.NoError(t, s.Ensure())
	require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 1}))
	var got map[string]int
	_, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, 1, got["value"])
}

func TestPlatformState_ValidateInput(t *testing.T) {
	s := New(Env{GOOS: "windows", Home: `C:\Users\test`, Root: t.TempDir(), Values: map[string]string{"APPDATA": `C:\Users\test\AppData\Roaming`}})
	require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 1}))
	require.NoError(t, s.Ensure())
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	require.NoError(t, err)
	acl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(filepath.Join(s.path, "hooks.json"), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
	var got map[string]int
	_, err = s.ReadJSON("hooks", &got)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}
