// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

// Native fixture replay preserves the captured SID and every raw SDDL string.
// LA denotes local administrator in the replay host's native SDDL context.
package oplocal

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestAncestor_CapturedStockWindowsFixture_ReplaysPolicy(t *testing.T) {
	capture, err := loadCapturedStockFixture()
	require.NoError(t, err)
	capturedSID, err := windows.StringToSid(capture.UserSID)
	require.NoError(t, err, "parse exact historical numeric SID")
	require.True(t, capturedSID.IsValid())
	require.Equal(t, capture.UserSID, capturedSID.String())
	localAdmin := stockFixtureLocalAdministrator(t)
	// SYNTHETIC unrelated numeric principal: neither captured nor host-local LA.
	unrelated, err := windows.StringToSid("S-1-5-21-1-2-3-1001")
	require.NoError(t, err)
	require.False(t, unrelated.Equals(capturedSID))
	require.False(t, unrelated.Equals(localAdmin))
	for _, row := range capture.Rows {
		t.Run(row.LogicalPath, func(t *testing.T) {
			descriptor, err := windows.SecurityDescriptorFromString(row.SDDL)
			require.NoError(t, err, "parse exact raw captured SDDL")
			principal := capturedSID
			if row.LogicalPath == "runner_profile" {
				principal = localAdmin
			}
			require.NoError(t, verifyAncestorAccess(descriptor, principal), "unchanged policy in explicit replay principal context")
			if row.LogicalPath == "runner_profile" {
				require.ErrorContains(t, verifyAncestorAccess(descriptor, unrelated), "ancestor write access is invalid")
				if !capturedSID.Equals(localAdmin) {
					require.ErrorContains(t, verifyAncestorAccess(descriptor, capturedSID), "ancestor write access is invalid")
					t.Logf("historical SID %s differs from replay-host LA %s; unchanged raw profile accepts only the explicit local alias context", capture.UserSID, localAdmin.String())
				} else {
					t.Logf("historical SID and replay-host LA both resolve to %s", capture.UserSID)
				}
			}
		})
	}
}

func stockFixtureLocalAdministrator(t *testing.T) *windows.SID {
	t.Helper()
	// SYNTHETIC owner-only descriptor resolves native LA. It is never captured
	// data and never replaces LA or any bytes in the historical descriptor.
	alias, err := windows.SecurityDescriptorFromString("O:LA")
	require.NoError(t, err, "resolve local administrator with native SDDL conversion")
	owner, _, err := alias.Owner()
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.True(t, owner.IsValid())
	require.True(t, strings.HasPrefix(owner.String(), "S-1-5-21-"), "LA must resolve to a local account SID")
	require.Equal(t, uint8(5), owner.SubAuthorityCount())
	require.Equal(t, uint32(500), owner.SubAuthority(4), "LA must retain administrator RID 500")
	principal, err := owner.Copy()
	require.NoError(t, err, "retain an independent local alias SID")
	return principal
}
