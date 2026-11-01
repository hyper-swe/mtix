// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Identity of a 0.5.x per-project daemon behind a PID file (ADR-007 D11, MTIX-112.17.15).
package main

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

// procLookup reads one process; procinfo.Info in production, a fake in tests.
type procLookup func(pid int) (procinfo.Proc, error)

// pidIsLegacyDaemon reports whether pid is the mtix daemon of the project rooted at projectRoot
// (argv: mtix [flags] daemon, or mtix [flags] sync daemon). The project a daemon serves is the
// nearest directory holding .mtix at or above its working directory: a -C value has already
// moved the daemon there, so no argument is parsed for it. A pid reused by any other program,
// including another mtix command (daemon install, status and the other service subcommands
// among them) or another project's daemon, is not live. Only an identity that cannot be read
// fails safe as live, including a daemon whose working directory cannot be read (darwin has no
// such read): skipping duty is recoverable, a double pull or hook firing is not.
func pidIsLegacyDaemon(lookup procLookup, pid int, projectRoot string) bool {
	proc, err := lookup(pid)
	if errors.Is(err, procinfo.ErrNoProcess) {
		return false
	}
	if err != nil {
		return true
	}
	known, isMtix := mtixIdentity(proc)
	if !known {
		return true
	}
	if !isMtix {
		return false
	}
	if len(proc.Args) == 0 {
		return true // an mtix process whose arguments are unreadable
	}
	if !runsDaemon(proc.Args) {
		return false
	}
	if proc.Cwd == "" {
		return true // the project cannot be determined
	}
	return servesProject(proc.Cwd, projectRoot)
}

// mtixIdentity reports whether any name the process table gives is known, and whether one of
// them is an mtix binary.
func mtixIdentity(proc procinfo.Proc) (known, isMtix bool) {
	for _, name := range []string{proc.Name, proc.Argv0, proc.Exe} {
		if name == "" {
			continue
		}
		known = true
		if strings.HasPrefix(procBase(name), "mtix") {
			isMtix = true
		}
	}
	return known, isMtix
}

// servesProject reports whether a daemon whose working directory is cwd serves projectRoot:
// the nearest directory at or above cwd that holds .mtix, as findMtixDir walks, equals the
// root once symlinks are resolved on both sides. No such directory means no project of this
// board.
func servesProject(cwd, projectRoot string) bool {
	for dir := cwd; ; {
		if info, err := os.Stat(filepath.Join(dir, ".mtix")); err == nil && info.IsDir() {
			return sameDirectory(dir, projectRoot)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func procBase(name string) string {
	return strings.ToLower(path.Base(strings.ReplaceAll(name, `\`, "/")))
}

// daemonServiceVerbs are the `mtix daemon` service-management subcommands; a process running
// one of them is not a daemon.
var daemonServiceVerbs = map[string]bool{"install": true, "uninstall": true, "start": true, "stop": true, "status": true, "help": true}

// runsDaemon reports whether args (argv[0] first) run the daemon itself: a daemon token (mtix
// daemon, or mtix sync daemon) that is not followed by a service verb. Flags may precede it,
// each with a value token of its own, so no list of value flags is kept; any other
// subcommand before it (mtix show daemon) means the process is not the daemon.
func runsDaemon(args []string) bool {
	afterFlag := false
	for i := 1; i < len(args); i++ {
		tok := args[i]
		switch {
		case strings.HasPrefix(tok, "-"):
			afterFlag = !strings.Contains(tok, "=") && len(tok) > 1 && (tok[1] == '-' || len(tok) == 2)
		case tok == "daemon":
			return i+1 >= len(args) || !daemonServiceVerbs[args[i+1]]
		case afterFlag || tok == "sync":
			afterFlag = false // a flag's value, or the sync group in front of daemon
		default:
			return false
		}
	}
	return false
}

// sameDirectory compares two directories after resolving symlinks where they exist.
func sameDirectory(a, b string) bool {
	return resolveDir(a) == resolveDir(b)
}

func resolveDir(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}
