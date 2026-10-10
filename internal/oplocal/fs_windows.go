// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package oplocal

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const stateAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff

func ntError(err error) error {
	var status windows.NTStatus
	if errors.As(err, &status) {
		return status.Errno()
	}
	return err
}

func userDescriptor() (*windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid := user.User.Sid.String()
	return windows.SecurityDescriptorFromString("O:" + sid + "D:P(A;;FA;;;" + sid + ")")
}

func relativeOpen(parent *os.File, name string, kind, disposition, access uint32) (*os.File, error) {
	text, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	sd, err := userDescriptor()
	if err != nil {
		return nil, err
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: text, Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE, SecurityDescriptor: sd}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var handle windows.Handle
	var result windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, access|windows.SYNCHRONIZE|windows.READ_CONTROL, &attrs, &result, nil, windows.FILE_ATTRIBUTE_NORMAL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, disposition, kind|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_WRITE_THROUGH, 0, 0)
	if err != nil {
		return nil, ntError(err)
	}
	f := os.NewFile(uintptr(handle), filepath.Join(parent.Name(), name))
	var info windows.ByHandleFileInformation
	if err = windows.GetFileInformationByHandle(handle, &info); err != nil || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, errors.Join(invalid("component metadata is invalid"), err, closeFile(f))
	}
	return f, nil
}

func openDirectory(path string, create bool, roots []string) (dir *os.File, err error) {
	roots, err = canonicalRoots(roots)
	if err != nil {
		return nil, err
	}
	volume := filepath.VolumeName(path)
	if volume == "" {
		return nil, invalid("directory must be absolute")
	}
	root, err := windows.UTF16PtrFromString(volume + `\`)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(root, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	dir = os.NewFile(uintptr(h), volume+`\`)
	return walkDirectory(dir, path, volume, create, roots)
}

func walkDirectory(dir *os.File, path, volume string, create bool, roots []string) (_ *os.File, err error) {
	parts := strings.Split(strings.TrimLeft(strings.TrimPrefix(filepath.Clean(path), volume), `\`), `\`)
	for _, part := range parts {
		if err = verifyAncestor(dir); err == nil {
			err = checkHandleLocation(dir, roots)
		}
		if err == nil {
			err = checkGit(dir)
		}
		if err != nil {
			break
		}
		disposition := uint32(windows.FILE_OPEN)
		if create {
			disposition = windows.FILE_OPEN_IF
		}
		var next *os.File
		next, err = relativeOpen(dir, part, windows.FILE_DIRECTORY_FILE, disposition, windows.GENERIC_READ)
		if err != nil {
			break
		}
		err = errors.Join(verifyAncestor(next), checkHandleLocation(next, roots), closeFile(dir))
		dir = next
		if err != nil {
			break
		}
	}
	if err == nil {
		err = checkGit(dir)
	}
	if err == nil {
		err = verifyFile(dir, true)
	}
	if err != nil {
		return nil, errors.Join(err, closeFile(dir))
	}
	return dir, nil
}

func checkGit(dir *os.File) error {
	for _, name := range []string{".git", ".mtix"} {
		f, err := relativeOpen(dir, name, 0, windows.FILE_OPEN, windows.GENERIC_READ)
		if err == nil {
			return errors.Join(invalid("directory must be outside a project or git work tree"), closeFile(f))
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("project metadata: %w", err)
		}
	}
	return nil
}

func verifyFile(f *os.File, directory bool) error {
	handle := windows.Handle(f.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return failure("metadata is unavailable", err)
	}
	if (info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory || info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return invalid("file type is invalid")
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return failure("access metadata is unavailable", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return failure("user identity is unavailable", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return failure("owner metadata is unavailable", err)
	}
	if owner == nil || !owner.Equals(user.User.Sid) {
		return invalid("owner is invalid")
	}
	return verifyAccess(sd, user.User.Sid)
}

func verifyAccess(sd *windows.SECURITY_DESCRIPTOR, user *windows.SID) error {
	control, _, err := sd.Control()
	if err != nil {
		return failure("access control is unavailable", err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return invalid("access rules are invalid")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return failure("access rules are unavailable", err)
	}
	if acl == nil || acl.AceCount != 1 {
		return invalid("access rules are invalid")
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err = windows.GetAce(acl, 0, &ace); err != nil {
		return failure("access entry is unavailable", err)
	}
	sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
	if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags != 0 || !sid.Equals(user) || uint32(ace.Mask)&stateAccess != stateAccess {
		return invalid("access entry is invalid")
	}
	return nil
}

func readData(dir *os.File, name string) (data []byte, err error) {
	f, err := relativeOpen(dir, name, windows.FILE_NON_DIRECTORY_FILE, windows.FILE_OPEN, windows.GENERIC_READ)
	if err != nil {
		return nil, fmt.Errorf("open state record: %w: %w", err, invalid("validate input"))
	}
	defer closeWith(f, &err)
	if err = verifyFile(f, false); err != nil {
		return nil, err
	}
	data, err = io.ReadAll(io.LimitReader(f, maxRecordSize+1))
	if err != nil {
		return nil, fmt.Errorf("read state: %w: %w", err, invalid("read failed"))
	}
	if len(data) > maxRecordSize {
		return nil, invalid("record is too large")
	}
	return data, nil
}
