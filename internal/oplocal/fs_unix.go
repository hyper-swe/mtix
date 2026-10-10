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
	var stat unix.Stat_t
	if err := unix.Fstat(checkedFD(f), &stat); err != nil {
		return failure("metadata is unavailable", err)
	}
	return validateMetadata(stat.Uid, uint32(stat.Mode), directory)
}

func validateMetadata(uid, actual uint32, directory bool) error {
	mode := uint32(0600)
	kind := uint32(unix.S_IFREG)
	if directory {
		mode = 0700
		kind = unix.S_IFDIR
	}
	effective := os.Geteuid()
	if effective < 0 || effective > math.MaxUint32 {
		return invalid("user identity is invalid")
	}
	if uid != uint32(effective) || actual&07777 != mode || actual&unix.S_IFMT != kind {
		return invalid("owner, permissions or type is invalid")
	}
	return nil
}

func openDirectory(path string, create bool) (dir *os.File, err error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir = descriptorFile(fd, "/")
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	for i, part := range parts {
		if err = checkGit(dir); err != nil {
			break
		}
		var next *os.File
		next, err = openComponent(dir, part, create)
		if err != nil {
			break
		}
		err = closeFile(dir)
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

func readData(dir *os.File, name string) (data []byte, err error) {
	fd, err := unix.Openat(checkedFD(dir), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open state record: %w: %w", err, invalid("validate input"))
	}
	f := descriptorFile(fd, name)
	defer closeWith(f, &err)
	if verifyErr := verifyFile(f, false); verifyErr != nil {
		return nil, verifyErr
	}
	data, err = io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil {
		return nil, fmt.Errorf("read state: %w: %w", err, invalid("read failed"))
	}
	if len(data) > 1024*1024 {
		return nil, invalid("record is too large")
	}
	return data, nil
}

func writeData(dir *os.File, name string, data []byte, exclusive bool, before func() error) (err error) {
	if validateErr := validateExisting(dir, name); validateErr != nil {
		return validateErr
	}
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return failure("temporary name is unavailable", err)
	}
	temp := ".state-" + hex.EncodeToString(random)
	fd, err := unix.Openat(checkedFD(dir), temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fmt.Errorf("create state: %w: %w", err, invalid("write failed"))
	}
	defer func() {
		e := unix.Unlinkat(checkedFD(dir), temp, 0)
		if !errors.Is(e, unix.ENOENT) {
			err = errors.Join(err, e)
		}
	}()
	f := descriptorFile(fd, temp)
	if err = verifyFile(f, false); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, closeFile(f))
	if err == nil && before != nil {
		err = before()
	}
	if err == nil {
		err = commitData(dir, temp, name, exclusive)
	}
	if err != nil {
		return fmt.Errorf("write state: %w: %w", err, invalid("write failed"))
	}
	return nil
}

func validateExisting(dir *os.File, name string) error {
	_, err := readData(dir, name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func commitData(dir *os.File, temp, name string, exclusive bool) error {
	var err error
	if exclusive {
		err = unix.Linkat(checkedFD(dir), temp, checkedFD(dir), name, 0)
	} else {
		err = unix.Renameat(checkedFD(dir), temp, checkedFD(dir), name)
	}
	if err != nil {
		return err
	}
	return dir.Sync()
}

func descriptorFile(fd int, name string) *os.File {
	if fd < 0 {
		return nil
	}
	return os.NewFile(uintptr(fd), name)
}
