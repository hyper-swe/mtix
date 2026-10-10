//go:build windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

func trustedSID(sid, user *windows.SID) bool {
	if sid == nil {
		return false
	}
	if sid.Equals(user) {
		return true
	}
	for _, kind := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		trusted, err := windows.CreateWellKnownSid(kind)
		if err == nil && sid.Equals(trusted) {
			return true
		}
	}
	return false
}

func verifyAncestor(dir *os.File) error {
	handle := windows.Handle(dir.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return failure("ancestor metadata is unavailable", err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return invalid("ancestor type is invalid")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return failure("ancestor access metadata is unavailable", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return failure("user identity is unavailable", err)
	}
	return verifyAncestorAccess(sd, user.User.Sid)
}

func verifyAncestorAccess(sd *windows.SECURITY_DESCRIPTOR, user *windows.SID) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return failure("ancestor owner metadata is unavailable", err)
	}
	if !trustedSID(owner, user) {
		return invalid("ancestor owner is invalid")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return failure("ancestor access rules are unavailable", err)
	}
	if acl == nil {
		return invalid("ancestor access rules are invalid")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err = windows.GetAce(acl, i, &ace); err != nil {
			return failure("ancestor access entry is unavailable", err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE && ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
			return invalid("ancestor access entry is unsupported")
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE || ace.Header.AceFlags&windows.INHERIT_ONLY_ACE != 0 {
			continue
		}
		const writes = windows.GENERIC_ALL | windows.GENERIC_WRITE | windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | 0x156
		if uint32(ace.Mask)&writes != 0 && !trustedSID((*windows.SID)(unsafe.Pointer(&ace.SidStart)), user) {
			return invalid("ancestor write access is invalid")
		}
	}
	return nil
}
