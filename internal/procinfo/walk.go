// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package procinfo

import "path"

// Proc is what the process table says about one process.
type Proc struct {
	PPID      int
	Name      string
	Argv0     string
	Exe       string
	StartTime string
}

// maxWalkDepth bounds the ancestor walk.
const maxWalkDepth = 8

// wrapperNames are shells and launch wrappers that live only as long as one
// command: a registration must outlive them, so the walk skips past them.
var wrapperNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true, "ksh": true,
	"env": true, "timeout": true, "nohup": true, "sudo": true,
}

func isWrapper(name string) bool {
	if len(name) > 0 && name[0] == '-' { // a login shell is named with a leading dash
		name = name[1:]
	}
	return wrapperNames[path.Base(name)]
}

// SessionProcess is the process found by the ancestor walk.
type SessionProcess struct {
	PID       int
	Name      string
	Harness   string // from Identify; "" when the process is not a known harness
	StartTime string
}

// runtimeNames are language runtimes that harnesses spawn as children: a node
// child of a known harness is part of that harness's session, not a session.
var runtimeNames = map[string]bool{"node": true, "bun": true, "deno": true}

// FindSession walks up from start (the parent of the calling process) to the
// nearest ancestor that is not a shell or wrapper and returns it with sure
// true. When the walk cannot be sure (depth bound, a read error, pid 1, or no
// readable start time) it returns only the starting pid, with no name and no
// start time, and sure false.
func FindSession(read func(pid int) (Proc, error), start int) (found SessionProcess, sure bool) {
	cur := start
	for depth := 0; depth < maxWalkDepth; depth++ {
		if cur <= 1 {
			return SessionProcess{PID: start}, false
		}
		p, err := read(cur)
		if err != nil {
			return SessionProcess{PID: start}, false
		}
		if !isWrapper(p.Name) {
			if p.StartTime == "" {
				return SessionProcess{PID: start}, false
			}
			if parent, ok := harnessParent(read, p); ok {
				return parent, true
			}
			return SessionProcess{PID: cur, Name: p.Name, Harness: Identify(p), StartTime: p.StartTime}, true
		}
		cur = p.PPID
	}
	return SessionProcess{PID: start}, false
}

// FindSessionProcess is FindSession reduced to pid and start time: an unsure
// walk returns the starting pid with an empty start time, which probes as
// unknown and fails safe (never replaced silently, --take-over still works).
func FindSessionProcess(read func(pid int) (Proc, error), start int) (pid int, startTime string, sure bool) {
	found, sure := FindSession(read, start)
	return found.PID, found.StartTime, sure
}

// harnessParent returns the parent when p is a language-runtime child (such as
// the node process Codex spawns) of a process that Identify knows.
func harnessParent(read func(pid int) (Proc, error), p Proc) (SessionProcess, bool) {
	if Identify(p) != "" || !runtimeNames[normalizeBase(p.Name)] || p.PPID <= 1 {
		return SessionProcess{}, false
	}
	parent, err := read(p.PPID)
	if err != nil || parent.StartTime == "" {
		return SessionProcess{}, false
	}
	h := Identify(parent)
	if h == "" {
		return SessionProcess{}, false
	}
	return SessionProcess{PID: p.PPID, Name: parent.Name, Harness: h, StartTime: parent.StartTime}, true
}
