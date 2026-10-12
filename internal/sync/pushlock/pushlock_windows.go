// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package pushlock

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// errPlatformLockHeld is the platform-specific sentinel returned when
// the LockFileEx attempt fails because another process holds the lock.
// pushlock.Acquire translates this into the public ErrLockHeld.
var errPlatformLockHeld = errors.New("platform: lock held")

const (
	lockfileExclusiveLock   = 0x00000002
	lockfileFailImmediately = 0x00000001
)

// platformAcquire attempts a non-blocking exclusive lock via LockFileEx.
// ERROR_LOCK_VIOLATION signals that another process holds the lock; any
// other Windows error is propagated verbatim.
func platformAcquire(f *os.File) error {
	handle := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		handle,
		lockfileExclusiveLock|lockfileFailImmediately,
		0, 1, 0, &overlapped,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
			errors.Is(err, windows.ERROR_IO_PENDING) {
			return errPlatformLockHeld
		}
		return err
	}
	return nil
}

// platformRelease releases the LockFileEx-held byte range.
func platformRelease(f *os.File) error {
	handle := windows.Handle(f.Fd())
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(handle, 0, 1, 0, &overlapped)
}

func platformOpenLock(path, name string) (held *os.File, err error) {
	text, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("data directory path: %w", err)
	}
	handle, err := windows.CreateFile(text, windows.GENERIC_READ|windows.READ_CONTROL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("open real data directory: %w", err)
	}
	dir := os.NewFile(uintptr(handle), path)
	defer func() {
		if closeErr := dir.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
			if held != nil {
				err = errors.Join(err, held.Close())
				held = nil
			}
		}
	}()
	if err := verifyLockObject(dir, true); err != nil {
		return nil, err
	}
	f, err := openRelativeLock(handle, name)
	if err != nil {
		return nil, err
	}
	if err := verifyLockObject(f, false); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func openRelativeLock(parent windows.Handle, name string) (*os.File, error) {
	text, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, fmt.Errorf("lock filename: %w", err)
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: text,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var handle windows.Handle
	var result windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&handle, windows.GENERIC_READ|windows.GENERIC_WRITE|windows.READ_CONTROL|windows.SYNCHRONIZE,
		&attrs, &result, nil, windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, windows.FILE_OPEN_IF,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	if err != nil {
		var status windows.NTStatus
		if errors.As(err, &status) {
			err = status.Errno()
		}
		return nil, fmt.Errorf("open real lock object: %w", err)
	}
	return os.NewFile(uintptr(handle), name), nil
}

func verifyLockObject(f *os.File, directory bool) error {
	handle := windows.Handle(f.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return fmt.Errorf("lock metadata: %w", err)
	}
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("lock owner metadata: %w", err)
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return fmt.Errorf("lock owner identity: %w", err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("effective user identity: %w", err)
	}
	return validateLockMetadata(info.FileAttributes, owner, user.User.Sid, directory)
}

func validateLockMetadata(attributes uint32, owner, effective *windows.SID, directory bool) error {
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory ||
		owner == nil || effective == nil || !owner.Equals(effective) {
		return fmt.Errorf("lock object must have expected type and effective user ownership")
	}
	return nil
}
