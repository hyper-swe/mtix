// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

const daemonLockFilename = "sync.daemon.lock"

var errDaemonLockHeld = errors.New("daemon lifetime lock held")

// daemonOwner keeps the persistent lock inode open throughout the native loop.
// Only a successfully acquired owner can publish or clean its diagnostic PID.
type daemonOwner struct {
	mu   sync.Mutex
	file *os.File
	dir  string
	pid  int
}

func acquireDaemonOwnership(dir string) (*daemonOwner, error) {
	if dir == "" {
		return nil, errors.New("daemon lock: project directory required")
	}
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("create daemon lock directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(dataDir, daemonLockFilename), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	if lockErr := platformAcquireDaemonLock(file); lockErr != nil {
		if closeErr := file.Close(); closeErr != nil {
			return nil, fmt.Errorf("close failed daemon lock: %w", closeErr)
		}
		return nil, fmt.Errorf("acquire daemon lock: %w", lockErr)
	}
	owner := &daemonOwner{file: file, dir: dir, pid: os.Getpid()}
	if err := writeDaemonPID(dir, owner.pid); err != nil {
		return nil, errors.Join(fmt.Errorf("publish daemon PID: %w", err), owner.release())
	}
	return owner, nil
}
func startDaemonOwnership(stderr io.Writer, label, dir string) (*daemonOwner, error) {
	owner, err := acquireDaemonOwnership(dir)
	if !errors.Is(err, errDaemonLockHeld) {
		return owner, err
	}
	// The OS lock is authority even when the diagnostic PID is absent or stale.
	pid, readErr := readDaemonPID(dir)
	if readErr != nil {
		return nil, readErr
	}
	fmt.Fprintf(stderr, "%s: already running (PID %d); exiting cleanly\n", label, pid)
	return nil, nil
}
func readDaemonPID(dir string) (int, error) {
	body, err := os.ReadFile(filepath.Join(dir, daemonPIDFilename))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read daemon PID: %w", err)
	}
	pid, parseErr := strconv.Atoi(string(body))
	if parseErr != nil {
		return 0, nil
	}
	return pid, nil
}
func (owner *daemonOwner) release() error {
	if owner == nil {
		return nil
	}
	owner.mu.Lock()
	defer owner.mu.Unlock()
	if owner.file == nil {
		return nil
	}
	// Check and remove while ownership is still held, then close even on error.
	cleanupErr := owner.removeMatchingPID()
	closeErr := owner.file.Close()
	owner.file = nil
	if closeErr != nil {
		closeErr = fmt.Errorf("close daemon lock: %w", closeErr)
	}
	return errors.Join(cleanupErr, closeErr)
}
func (owner *daemonOwner) removeMatchingPID() error {
	path := filepath.Join(owner.dir, daemonPIDFilename)
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read owned daemon PID: %w", err)
	}
	if string(body) != strconv.Itoa(owner.pid) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove owned daemon PID: %w", err)
	}
	return nil
}
