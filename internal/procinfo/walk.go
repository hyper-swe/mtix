// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package procinfo

import "path"

// Proc is what the process table says about one process.
type Proc struct {
	PPID      int
	Name      string
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

// FindSessionProcess walks up from start (the parent of the calling process)
// to the nearest ancestor that is not a shell or wrapper, and returns its pid
// and start time with sure true. When the walk cannot be sure (depth bound,
// a read error, pid 1, or no readable start time) it returns the starting pid
// with an empty start time and sure false, so the row probes as unknown and
// fails safe: no silent replacement, and --take-over still works.
func FindSessionProcess(read func(pid int) (Proc, error), start int) (pid int, startTime string, sure bool) {
	cur := start
	for depth := 0; depth < maxWalkDepth; depth++ {
		if cur <= 1 {
			return start, "", false
		}
		p, err := read(cur)
		if err != nil {
			return start, "", false
		}
		if !isWrapper(p.Name) {
			if p.StartTime == "" {
				return start, "", false
			}
			return cur, p.StartTime, true
		}
		cur = p.PPID
	}
	return start, "", false
}
