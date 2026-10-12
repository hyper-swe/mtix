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
	"unsafe"

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

// TestNamedLock_WindowsAdminGate_ElevationAndOwnerFlags tables the token inputs
// behind the BUILTIN/Administrators owner exception: it needs an elevated
// token whose Administrators group is enabled, marked owner and not deny-only.
func TestNamedLock_WindowsAdminGate_ElevationAndOwnerFlags(t *testing.T) {
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	require.NoError(t, err)
	other, err := windows.CreateWellKnownSid(windows.WinWorldSid)
	require.NoError(t, err)
	const enabledOwner = windows.SE_GROUP_ENABLED | windows.SE_GROUP_OWNER
	cases := []struct {
		name     string
		elevated bool
		group    *windows.SID
		flags    uint32
		want     bool
	}{
		{"elevated-enabled-owner", true, admin, enabledOwner, true},
		{"not-elevated", false, admin, enabledOwner, false},
		{"elevated-not-enabled", true, admin, windows.SE_GROUP_OWNER, false},
		{"elevated-not-owner", true, admin, windows.SE_GROUP_ENABLED, false},
		{"elevated-deny-only", true, admin, windows.SE_GROUP_USE_FOR_DENY_ONLY, false},
		{"elevated-enabled-owner-deny-only", true, admin, enabledOwner | windows.SE_GROUP_USE_FOR_DENY_ONLY, false},
		{"elevated-other-group", true, other, enabledOwner, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groups := []windows.SIDAndAttributes{{Sid: tc.group, Attributes: tc.flags}}
			require.Equal(t, tc.want, adminOwnerAllowed(tc.elevated, groups, admin))
		})
	}
	require.False(t, adminOwnerAllowed(true, nil, admin), "no groups never allows")
}

// TestNamedLock_WindowsLockFile_UserOnlyAccessAndNoDelete proves a held lock
// file cannot be renamed or deleted (no FILE_SHARE_DELETE) and that the file
// created by AcquireNamed carries a protected access list naming only the
// token user, so no other principal can open, rename or delete it.
func TestNamedLock_WindowsLockFile_UserOnlyAccessAndNoDelete(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	root := t.TempDir()
	lock, err := AcquireNamed(root, "access.lock")
	require.NoError(t, err)
	path := filepath.Join(root, pushLockSubdir, "access.lock")
	require.Error(t, os.Rename(path, path+".moved"), "held lock cannot be renamed")
	require.Error(t, os.Remove(path), "held lock cannot be deleted")
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)
	control, _, err := sd.Control()
	require.NoError(t, err)
	require.NotZero(t, control&windows.SE_DACL_PROTECTED, "access list must not inherit")
	acl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NotNil(t, acl)
	require.EqualValues(t, 1, acl.AceCount)
	var ace *windows.ACCESS_ALLOWED_ACE
	require.NoError(t, windows.GetAce(acl, 0, &ace))
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	require.True(t, sid.Equals(user.User.Sid), "only the token user is named")
	require.NoError(t, lock.Release())
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "released lock file stays in place")
}
