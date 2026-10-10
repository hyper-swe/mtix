// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package oplocal

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestState_ProtectedAccess_RoundTrips(t *testing.T) {
	root := safeFixtureRoot(t)
	s := New(Env{GOOS: "windows", Home: `C:\Users\test`, root: root, Values: map[string]string{"APPDATA": `C:\Users\test\AppData\Roaming`, "TMPDIR": `C:\Temp`}})
	require.NoError(t, s.Ensure())
	require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 1}))
	var got map[string]int
	_, err := s.ReadJSON("hooks", &got)
	require.NoError(t, err)
	require.Equal(t, 1, got["value"])
}

func TestReadJSON_WideAccess_Refuses(t *testing.T) {
	s := New(Env{GOOS: "windows", Home: `C:\Users\test`, root: safeFixtureRoot(t), Values: map[string]string{"APPDATA": `C:\Users\test\AppData\Roaming`}})
	require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 1}))
	require.NoError(t, s.Ensure())
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	require.NoError(t, err)
	acl, _, err := sd.DACL()
	require.NoError(t, err)
	require.NoError(t, windows.SetNamedSecurityInfo(filepath.Join(s.pathForTest(t), "hooks.json"), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
	var got map[string]int
	_, err = s.ReadJSON("hooks", &got)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestAncestor_TrusteeAndOwnerAccess_ValidatesPlatformPrincipals(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	require.NoError(t, err)
	sid := user.User.Sid.String()
	for _, tc := range []struct {
		name, owner, access string
		allow               bool
	}{
		{"current_user", sid, "(A;;FA;;;" + sid + ")", true},
		{"system", "SY", "(A;;FA;;;SY)(A;;FR;;;WD)", true},
		{"administrators", "BA", "(A;;FA;;;BA)", true},
		{"everyone_write", sid, "(A;;FA;;;WD)", false},
		{"users_delete", sid, "(A;;SD;;;BU)", false},
		{"foreign_owner", "BU", "(A;;FR;;;WD)", false},
		{"inherit_only", sid, "(A;IO;FA;;;WD)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sd, parseErr := windows.SecurityDescriptorFromString("O:" + tc.owner + "D:" + tc.access)
			require.NoError(t, parseErr)
			checkErr := verifyAncestorAccess(sd, user.User.Sid)
			if tc.allow {
				require.NoError(t, checkErr)
			} else {
				require.ErrorIs(t, checkErr, model.ErrOperatorStateUnreadable)
			}
		})
	}
}
func TestEnsure_CanonicalHandleAlias_Refuses(t *testing.T) {
	root := safeFixtureRoot(t)
	target := filepath.Join(root, "harness-state")
	require.NoError(t, os.Mkdir(target, 0700))
	for _, prefix := range []string{"", `\\?\`} {
		_, err := openDirectory(prefix+target, true, []string{target})
		require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	}
	text, err := windows.UTF16PtrFromString(target)
	require.NoError(t, err)
	buffer := make([]uint16, 32768)
	count, err := windows.GetShortPathName(text, &buffer[0], uint32(len(buffer)))
	require.NoError(t, err)
	short := windows.UTF16ToString(buffer[:count])
	if strings.EqualFold(short, target) {
		t.Skip("short path names are unavailable on this volume")
	}
	_, err = openDirectory(short, true, []string{target})
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}
