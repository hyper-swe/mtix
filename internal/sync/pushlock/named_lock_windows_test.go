// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

// Windows controls retain synthetic metadata qualification; actual symlink
// and owned-handle acquisition cases live in the portable named-lock tests.
package pushlock

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
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
	identity := lockIdentity{user: user.User.Sid}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// SYNTHETIC metadata rows, not creation of a foreign-owned native file.
			err := validateLockMetadata(tc.attributes, tc.owner, identity, tc.directory)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	require.Error(t, validateLockMetadata(0, user.User.Sid, lockIdentity{}, false))
}

// TestNamedLock_WindowsOwnerPolicy_AcceptsUserAndElevatedAdministrators covers
// the three owner cases: the token user, BUILTIN/Administrators only for an
// elevated Administrators token, and every other owner refused.
func TestNamedLock_WindowsOwnerPolicy_AcceptsUserAndElevatedAdministrators(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	require.NoError(t, err)
	other, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	require.NoError(t, err)
	cases := []struct {
		name     string
		owner    *windows.SID
		elevated bool
		wantErr  bool
	}{
		{"token-user", user.User.Sid, false, false},
		{"administrators-elevated", admin, true, false},
		{"administrators-not-elevated", admin, false, true},
		{"other-owner-elevated", other, true, true},
		{"other-owner", other, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			identity := lockIdentity{user: user.User.Sid, admin: admin, adminOwnerAllowed: tc.elevated}
			err := validateLockMetadata(windows.FILE_ATTRIBUTE_NORMAL, tc.owner, identity, false)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
	actual, err := currentLockIdentity()
	require.NoError(t, err)
	require.True(t, actual.user.Equals(user.User.Sid))
	require.True(t, actual.admin.Equals(admin))
}

// TestNamedLock_WindowsOwner_RealObjectsOwnedByUser reads the owner of a real
// AcquireNamed directory and file. They must be owned by the token user, not
// the BUILTIN/Administrators default that elevated tokens assign to new objects.
func TestNamedLock_WindowsOwner_RealObjectsOwnedByUser(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	root := t.TempDir()
	lock, err := AcquireNamed(root, "owner.lock")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })
	for _, path := range []string{filepath.Join(root, pushLockSubdir), filepath.Join(root, pushLockSubdir, "owner.lock")} {
		sd, sdErr := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
		require.NoError(t, sdErr)
		owner, _, ownerErr := sd.Owner()
		require.NoError(t, ownerErr)
		require.Truef(t, owner.Equals(user.User.Sid), "%s owner %s must be the token user %s", path, owner, user.User.Sid)
	}
}

// TestNamedLock_WindowsLegacyDirectoryAndForeignDirectory exercises existing
// objects: a data directory made by an older run with default ownership
// (Administrators on an elevated token) is accepted, and a real directory owned
// by neither the user nor an elevated Administrators token is refused.
func TestNamedLock_WindowsLegacyDirectoryAndForeignDirectory(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, pushLockSubdir), 0o755))
	lock, err := AcquireNamed(root, "legacy.lock")
	require.NoError(t, err, "default-owned data directory from an older run stays usable")
	require.NoError(t, lock.Release())
	system := os.Getenv("SystemRoot")
	require.NotEmpty(t, system)
	foreign, err := platformOpenLock(system, "foreign.lock")
	require.Error(t, err)
	require.Nil(t, foreign)
	require.Contains(t, err.Error(), "ownership")
}
