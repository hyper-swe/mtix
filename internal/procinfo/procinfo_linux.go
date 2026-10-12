// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package procinfo

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
)

// StartTime returns the process start time as clock ticks since boot, read
// from field 22 of /proc/<pid>/stat.
func StartTime(pid int) (string, error) {
	if pid <= 0 {
		return "", ErrNoProcess
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") // #nosec G304 -- pid is an integer
	if errors.Is(err, fs.ErrNotExist) {
		return "", ErrNoProcess
	}
	if err != nil {
		return "", fmt.Errorf("read process stat: %w", err)
	}
	return parseStatStart(data)
}

// parseStatStart extracts field 22; the command name (field 2) may contain
// spaces and parentheses, so parsing starts after its last closing parenthesis.
func parseStatStart(data []byte) (string, error) {
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return "", fmt.Errorf("malformed process stat")
	}
	fields := bytes.Fields(data[end+1:])
	// fields[0] is field 3 (state); field 22 is fields[19].
	if len(fields) < 20 {
		return "", fmt.Errorf("malformed process stat")
	}
	return string(fields[19]), nil
}

// Info reads the parent pid, command name and start time of pid from
// /proc/<pid>/stat.
func Info(pid int) (Proc, error) {
	if pid <= 0 {
		return Proc{}, ErrNoProcess
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat") // #nosec G304 -- pid is an integer
	if errors.Is(err, fs.ErrNotExist) {
		return Proc{}, ErrNoProcess
	}
	if err != nil {
		return Proc{}, fmt.Errorf("read process stat: %w", err)
	}
	return parseStatInfo(data)
}

func parseStatInfo(data []byte) (Proc, error) {
	open, end := bytes.IndexByte(data, '('), bytes.LastIndexByte(data, ')')
	if open < 0 || end < open {
		return Proc{}, fmt.Errorf("malformed process stat")
	}
	fields := bytes.Fields(data[end+1:])
	if len(fields) < 20 {
		return Proc{}, fmt.Errorf("malformed process stat")
	}
	ppid, err := strconv.Atoi(string(fields[1])) // field 4
	if err != nil {
		return Proc{}, fmt.Errorf("malformed process stat: %w", err)
	}
	return Proc{PPID: ppid, Name: string(data[open+1 : end]), StartTime: string(fields[19])}, nil
}
