// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package pushlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/hyper-swe/mtix/internal/oplocal"
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

// platformEnsureDir creates the lock data directory when it is absent, with an
// explicit owner of the current token user. Without it an elevated
// Administrators token would own the new directory as BUILTIN\Administrators.
func platformEnsureDir(dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	sd, err := oplocal.OwnerDescriptor()
	if err != nil {
		return err
	}
	text, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("data directory path: %w", err)
	}
	attrs := windows.SecurityAttributes{SecurityDescriptor: sd}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	err = windows.CreateDirectory(text, &attrs)
	runtime.KeepAlive(sd)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return err
	}
	return nil
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
	if verifyErr := verifyLockObject(dir, true); verifyErr != nil {
		return nil, verifyErr
	}
	f, err := openRelativeLock(handle, name)
	if err != nil {
		return nil, err
	}
	if verifyErr := verifyLockObject(f, false); verifyErr != nil {
		return nil, errors.Join(verifyErr, f.Close())
	}
	return f, nil
}

func openRelativeLock(parent windows.Handle, name string) (*os.File, error) {
	text, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, fmt.Errorf("lock filename: %w", err)
	}
	sd, err := oplocal.OwnerDescriptor()
	if err != nil {
		return nil, err
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: parent, ObjectName: text, SecurityDescriptor: sd,
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
	identity, err := currentLockIdentity()
	if err != nil {
		return err
	}
	return validateLockMetadata(info.FileAttributes, owner, identity, directory)
}

// lockIdentity is the ownership policy for existing lock objects (FR-18.18).
// An owner is accepted when it is the token user, or when it is
// BUILTIN\Administrators and adminOwnerAllowed holds. adminOwnerAllowed is true
// only for an elevated token whose Administrators group is enabled with
// SE_GROUP_OWNER, so data directories made by older elevated runs (which
// defaulted to the Administrators owner) stay usable. Every other owner is
// refused.
type lockIdentity struct {
	user, admin       *windows.SID
	adminOwnerAllowed bool
}

func currentLockIdentity() (lockIdentity, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return lockIdentity{}, fmt.Errorf("effective user identity: %w", err)
	}
	admin, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return lockIdentity{}, fmt.Errorf("administrators identity: %w", err)
	}
	identity := lockIdentity{user: user.User.Sid, admin: admin}
	if !token.IsElevated() {
		return identity, nil
	}
	groups, err := token.GetTokenGroups()
	if err != nil {
		return lockIdentity{}, fmt.Errorf("token groups: %w", err)
	}
	const needed = windows.SE_GROUP_ENABLED | windows.SE_GROUP_OWNER
	for _, group := range groups.AllGroups() {
		if group.Sid.Equals(admin) && group.Attributes&needed == needed {
			identity.adminOwnerAllowed = true
		}
	}
	return identity, nil
}

func validateLockMetadata(attributes uint32, owner *windows.SID, identity lockIdentity, directory bool) error {
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		(attributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0) != directory ||
		owner == nil || identity.user == nil || !lockOwnerAccepted(owner, identity) {
		return fmt.Errorf("lock object must have expected type and effective user ownership")
	}
	return nil
}

func lockOwnerAccepted(owner *windows.SID, identity lockIdentity) bool {
	return owner.Equals(identity.user) ||
		(identity.adminOwnerAllowed && identity.admin != nil && owner.Equals(identity.admin))
}
