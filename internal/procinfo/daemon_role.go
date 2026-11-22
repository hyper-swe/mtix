// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package procinfo

import (
	"path/filepath"
	"slices"
)

// DaemonRole is what a process holding the board's daemon lock is, judged
// from its arguments alone (MTIX-276).
type DaemonRole int

const (
	// RoleUnknown means the process could not be proven to be an mtix daemon
	// or listener of this project. Callers fail closed: they signal nothing.
	RoleUnknown DaemonRole = iota
	// RoleListener is the detached `mtix daemon --listen` a waiter starts.
	RoleListener
	// RoleDaemon is a daemon that is not a listener: the OS service or a
	// foreground `mtix daemon` or `mtix sync daemon`.
	RoleDaemon
)

// listenerArgs returns the exact argument list of a spawned listener (it
// mirrors cmd/mtix listenerArgv, which is fixed and carries nothing else).
func listenerArgs() []string { return []string{"daemon", "--listen"} }

// ClassifyMtixDaemon says whether p is this project's detached listener, a
// daemon of another kind, or unknown, and why when unknown. A listener must be
// an mtix binary whose arguments are exactly `daemon --listen`; where the
// kernel reports the working directory (linux) it must be projectDir. A
// process whose arguments cannot be read is never classified (MTIX-276).
func ClassifyMtixDaemon(p Proc, projectDir string) (DaemonRole, string) {
	if len(p.Args) == 0 {
		return RoleUnknown, "the process arguments cannot be read"
	}
	if base := normalizeBase(p.Args[0]); base != "mtix" && base != "mtix.exe" {
		return RoleUnknown, "the process is not an mtix binary"
	}
	if p.Cwd != "" && projectDir != "" && filepath.Clean(p.Cwd) != filepath.Clean(projectDir) {
		return RoleUnknown, "the process runs in another directory"
	}
	rest := p.Args[1:]
	switch {
	case slices.Equal(rest, listenerArgs()):
		return RoleListener, ""
	case slices.Contains(rest, "--listen"):
		return RoleUnknown, "the process has listener flags beyond `daemon --listen`"
	case len(rest) > 0 && rest[0] == "daemon", len(rest) > 1 && rest[0] == "sync" && rest[1] == "daemon":
		return RoleDaemon, ""
	}
	return RoleUnknown, "the process is not an mtix daemon"
}
