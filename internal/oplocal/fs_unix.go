// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func checkedFD(f *os.File) int { return int(f.Fd()) } //nolint:gosec // File descriptors originate from operating system opens.

func verifyFile(f *os.File, directory bool) error {
	if directory {
		return verifyKind(f, 0700, unix.S_IFDIR)
	}
	return verifyKind(f, recordMode, unix.S_IFREG)
}

// recordMode is the mode of every operator-local record; executableMode is
// the mode of a release-defined executable such as the attach shim.
const (
	recordMode     = 0600
	executableMode = 0700
)

func verifyKind(f *os.File, mode, kind uint32) error {
	var stat unix.Stat_t
	if err := unix.Fstat(checkedFD(f), &stat); err != nil {
		return failure("metadata is unavailable", err)
	}
	return validateKind(stat.Uid, uint32(stat.Mode), mode, kind)
}

func validateMetadata(uid, actual uint32, directory bool) error {
	if directory {
		return validateKind(uid, actual, 0700, unix.S_IFDIR)
	}
	return validateKind(uid, actual, recordMode, unix.S_IFREG)
}

func validateKind(uid, actual, mode, kind uint32) error {
	effective := os.Geteuid()
	if effective < 0 || effective > math.MaxUint32 {
		return invalid("user identity is invalid")
	}
	if uid != uint32(effective) || actual&07777 != mode || actual&unix.S_IFMT != kind {
		return invalid("owner, permissions or type is invalid")
	}
	return nil
}

func openDirectory(path string, create bool, roots []string) (dir *os.File, err error) {
	if locationErr := checkUnixLocation(path, roots); locationErr != nil {
		return nil, locationErr
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir = descriptorFile(fd, "/")
	return walkDirectory(dir, path, create)
}

func walkDirectory(dir *os.File, path string, create bool) (_ *os.File, err error) {
	return walkDirectoryChecked(dir, path, create, verifyAncestor)
}

func walkDirectoryChecked(dir *os.File, path string, create bool, check func(*os.File) error) (_ *os.File, err error) {
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	for i, part := range parts {
		if err = check(dir); err == nil {
			err = checkGit(dir)
		}
		if err != nil {
			break
		}
		var next *os.File
		next, err = openComponent(dir, part, create)
		if err != nil {
			break
		}
		err = errors.Join(check(next), closeFile(dir))
		dir = next
		if err != nil {
			break
		}
		if i == len(parts)-1 {
			err = verifyFile(dir, true)
			if err == nil {
				err = checkGit(dir)
			}
		}
	}
	if err != nil {
		return nil, errors.Join(err, closeFile(dir))
	}
	return dir, nil
}

func checkGit(dir *os.File) error {
	for _, name := range []string{".git", ".mtix"} {
		var stat unix.Stat_t
		err := unix.Fstatat(checkedFD(dir), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil {
			return invalid("directory must be outside a project or git work tree")
		}
		if !errors.Is(err, unix.ENOENT) {
			return failure("project metadata is unavailable", err)
		}
	}
	return nil
}

func openComponent(parent *os.File, name string, create bool) (*os.File, error) {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_NOFOLLOW | unix.O_CLOEXEC
	fd, err := unix.Openat(checkedFD(parent), name, flags, 0)
	if errors.Is(err, unix.ENOENT) && create {
		if err = unix.Mkdirat(checkedFD(parent), name, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, err
		}
		fd, err = unix.Openat(checkedFD(parent), name, flags, 0)
	}
	if err != nil {
		return nil, err
	}
	return descriptorFile(fd, filepath.Join(parent.Name(), name)), nil
}

func readData(dir *os.File, name string) ([]byte, error) {
	return readDataMode(dir, name, recordMode)
}

func readDataMode(dir *os.File, name string, mode uint32) (data []byte, err error) {
	fd, err := unix.Openat(checkedFD(dir), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open state record: %w: %w", err, invalid("validate input"))
	}
	f := descriptorFile(fd, name)
	defer closeWith(f, &err)
	if verifyErr := verifyKind(f, mode, unix.S_IFREG); verifyErr != nil {
		return nil, verifyErr
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

func writeData(dir *os.File, name string, data []byte, exclusive bool, before func() error, syncDirectory func(*os.File) error) error {
	return writeDataMode(dir, name, data, writeOptions{exclusive: exclusive, before: before, syncDirectory: syncDirectory, mode: recordMode})
}

type writeOptions struct {
	exclusive     bool
	before        func() error
	syncDirectory func(*os.File) error
	mode          uint32
}

func writeDataMode(dir *os.File, name string, data []byte, opts writeOptions) (err error) {
	if validateErr := validateExistingMode(dir, name, opts.mode); validateErr != nil {
		return validateErr
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return failure("temporary name is unavailable", err)
	}
	temp := ".state-" + hex.EncodeToString(random)
	fd, err := unix.Openat(checkedFD(dir), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, opts.mode)
	if err != nil {
		return fmt.Errorf("create state: %w: %w", err, invalid("write failed"))
	}
	defer func() {
		e := unix.Unlinkat(checkedFD(dir), temp, 0)
		if !errors.Is(e, unix.ENOENT) {
			if e != nil {
				err = errors.Join(err, failure("temporary record cleanup failed", e))
			}
		}
	}()
	f := descriptorFile(fd, temp)
	if err = verifyKind(f, opts.mode, unix.S_IFREG); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, closeFile(f))
	if err == nil && opts.before != nil {
		err = opts.before()
	}
	if err == nil {
		err = commitData(dir, temp, name, opts.exclusive, opts.syncDirectory)
	}
	if err != nil {
		return fmt.Errorf("write state: %w: %w", err, invalid("write failed"))
	}
	return nil
}

func validateExistingMode(dir *os.File, name string, mode uint32) error {
	_, err := readDataMode(dir, name, mode)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func commitData(dir *os.File, temp, name string, exclusive bool, syncDirectory func(*os.File) error) error {
	var err error
	if exclusive {
		err = unix.Linkat(checkedFD(dir), temp, checkedFD(dir), name, 0)
	} else {
		err = unix.Renameat(checkedFD(dir), temp, checkedFD(dir), name)
	}
	if err != nil {
		if exclusive {
			return failure("create exclusive state record using a hard link", err)
		}
		return failure("commit state record", err)
	}
	if syncDirectory != nil {
		return syncDirectory(dir)
	}
	return dir.Sync()
}

func descriptorFile(fd int, name string) *os.File {
	if fd < 0 {
		return nil
	}
	return os.NewFile(uintptr(fd), name)
}

// removeData deletes one state record relative to the verified directory.
func removeData(dir *os.File, name string) error {
	if _, err := readData(dir, name); err != nil {
		return err
	}
	if err := unix.Unlinkat(checkedFD(dir), name, 0); err != nil {
		return failure("remove state record", err)
	}
	return dir.Sync()
}
