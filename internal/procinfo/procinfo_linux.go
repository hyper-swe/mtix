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
	"strings"
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

// Info reads the parent pid, command name, start time, executable path and
// argv[0] of pid from /proc. The executable and argv[0] are best effort: they
// stay empty when the process belongs to another user.
func Info(pid int) (Proc, error) {
	if pid <= 0 {
		return Proc{}, ErrNoProcess
	}
	dir := "/proc/" + strconv.Itoa(pid)
	data, err := os.ReadFile(dir + "/stat") // #nosec G304 -- pid is an integer
	if errors.Is(err, fs.ErrNotExist) {
		return Proc{}, ErrNoProcess
	}
	if err != nil {
		return Proc{}, fmt.Errorf("read process stat: %w", err)
	}
	p, err := parseStatInfo(data)
	if err != nil {
		return Proc{}, err
	}
	p.Exe, _ = os.Readlink(dir + "/exe")                             // best effort: another user's process is unreadable
	if cmdline, rerr := os.ReadFile(dir + "/cmdline"); rerr == nil { // #nosec G304 -- pid is an integer
		p.Argv0, _, _ = strings.Cut(string(cmdline), "\x00")
		p.Args = splitCmdline(cmdline)
	}
	p.Cwd, _ = os.Readlink(dir + "/cwd") // best effort: another user's process is unreadable
	return p, nil
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

// splitCmdline splits a /proc/<pid>/cmdline buffer into its arguments. An empty
// buffer (a zombie, or a kernel thread) yields nil: the arguments are unknown.
func splitCmdline(raw []byte) []string {
	raw = bytes.TrimSuffix(raw, []byte{0})
	if len(raw) == 0 {
		return nil
	}
	return strings.Split(string(raw), "\x00")
}
