// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package procinfo

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"
)

// StartTime returns the process start time (seconds and microseconds since the
// epoch) from the kernel process table via sysctl.
func StartTime(pid int) (string, error) {
	if pid <= 0 {
		return "", ErrNoProcess
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) ||
			errors.Is(err, unix.EIO) { // x/sys reports EIO when the kernel returns no record for the pid
			return "", ErrNoProcess
		}
		return "", fmt.Errorf("read process table: %w", err)
	}
	// An exited pid can return an empty record rather than an error.
	if int(info.Proc.P_pid) != pid {
		return "", ErrNoProcess
	}
	st := info.Proc.P_starttime
	return strconv.FormatInt(int64(st.Sec), 10) + "." + strconv.FormatInt(int64(st.Usec), 10), nil
}

// Info reads the parent pid, command name and start time of pid from the
// kernel process table.
func Info(pid int) (Proc, error) {
	if pid <= 0 {
		return Proc{}, ErrNoProcess
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) || errors.Is(err, unix.EIO) {
			return Proc{}, ErrNoProcess
		}
		return Proc{}, fmt.Errorf("read process table: %w", err)
	}
	if int(info.Proc.P_pid) != pid {
		return Proc{}, ErrNoProcess
	}
	comm := info.Proc.P_comm[:]
	for i, c := range comm {
		if c == 0 {
			comm = comm[:i]
			break
		}
	}
	st := info.Proc.P_starttime
	return Proc{PPID: int(info.Eproc.Ppid), Name: string(comm),
		StartTime: strconv.FormatInt(int64(st.Sec), 10) + "." + strconv.FormatInt(int64(st.Usec), 10)}, nil
}
