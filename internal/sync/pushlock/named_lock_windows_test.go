// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

// Windows controls retain synthetic metadata qualification; actual symlink
// and owned-handle acquisition cases live in the portable named-lock tests.
package pushlock

import (
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
	"testing"
)

func TestNamedLock_WindowsMetadata_ValidatesTypeAndOwner(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	other, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	require.NoError(t, err)
	cases := []struct {
		name       string
		attributes uint32
		owner      *windows.SID
		directory  bool
		wantErr    bool
	}{
		{"owned-dir", windows.FILE_ATTRIBUTE_DIRECTORY, user.User.Sid, true, false},
		{"owned-file", windows.FILE_ATTRIBUTE_NORMAL, user.User.Sid, false, false},
		{"reparse-dir", windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_REPARSE_POINT, user.User.Sid, true, true},
		{"reparse-file", windows.FILE_ATTRIBUTE_REPARSE_POINT, user.User.Sid, false, true},
		{"foreign-owner", windows.FILE_ATTRIBUTE_NORMAL, other, false, true},
		{"nil-owner", windows.FILE_ATTRIBUTE_NORMAL, nil, false, true},
		{"wrong-type", windows.FILE_ATTRIBUTE_DIRECTORY, user.User.Sid, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// SYNTHETIC metadata rows, not creation of a foreign-owned native file.
			err := validateLockMetadata(tc.attributes, tc.owner, user.User.Sid, tc.directory)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	require.Error(t, validateLockMetadata(0, user.User.Sid, nil, false))
}
