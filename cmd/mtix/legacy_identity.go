// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Identity of a 0.5.x per-project daemon behind a PID file (ADR-007 D11, MTIX-112.17.15).
package main

import (
	"errors"
	"path"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

// procLookup reads one process; procinfo.Info in production, a fake in tests.
type procLookup func(pid int) (procinfo.Proc, error)

// pidIsLegacyDaemon reports whether pid is the mtix daemon of the project rooted at projectRoot
// (argv: mtix [-C root] daemon, or mtix [-C root] sync daemon). A pid reused by any other
// program, including another mtix command (daemon install, status and the other service
// subcommands among them) or another project's daemon, is not live. Only an identity that
// cannot be read fails safe as live, including a daemon whose project cannot be determined
// (no -C and an unreadable working directory): skipping duty is recoverable, a double pull or
// hook firing is not.
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
	daemon, root := parseDaemonArgs(proc.Args)
	if !daemon {
		return false
	}
	root, resolvable := daemonProjectDir(root, proc.Cwd)
	return !resolvable || sameDirectory(root, projectRoot)
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

// daemonProjectDir resolves the project directory a daemon serves: the -C value, taken against
// the daemon's own working directory when relative, else that working directory. ok is false
// when the directory cannot be determined, which the caller treats as live.
func daemonProjectDir(flagRoot, cwd string) (dir string, ok bool) {
	switch {
	case flagRoot == "":
		return cwd, cwd != ""
	case filepath.IsAbs(flagRoot):
		return flagRoot, true
	case cwd == "":
		return "", false
	}
	return filepath.Join(cwd, flagRoot), true
}

func procBase(name string) string {
	return strings.ToLower(path.Base(strings.ReplaceAll(name, `\`, "/")))
}

// daemonServiceVerbs are the `mtix daemon` service-management subcommands; a process running
// one of them is not a daemon.
var daemonServiceVerbs = map[string]bool{"install": true, "uninstall": true, "start": true, "stop": true, "status": true, "help": true}

// parseDaemonArgs reports whether args (argv[0] first) run the daemon itself, and the project
// directory given by -C / --project-dir ("" when absent). Root flags may precede the
// subcommand, and the value of --log-level is not a subcommand.
func parseDaemonArgs(args []string) (daemon bool, root string) {
	var positional []string
	for i := 1; i < len(args) && len(positional) < 3; i++ {
		tok := args[i]
		if value, skip, ok := projectDirFlag(args, i); ok {
			root = value
			i += skip
		} else if tok == "--log-level" {
			i++
		} else if !strings.HasPrefix(tok, "-") {
			positional = append(positional, tok)
		}
	}
	if len(positional) > 0 && positional[0] == "sync" {
		positional = positional[1:]
	}
	if len(positional) == 0 || positional[0] != "daemon" {
		return false, root
	}
	return len(positional) < 2 || !daemonServiceVerbs[positional[1]], root
}

// projectDirFlag recognizes -C x, --project-dir x, --project-dir=x, -Cx and -C=x at args[i];
// skip is how many following tokens the flag consumed.
func projectDirFlag(args []string, i int) (value string, skip int, ok bool) {
	tok := args[i]
	switch {
	case tok == "-C" || tok == "--project-dir":
		if i+1 < len(args) {
			return args[i+1], 1, true
		}
		return "", 0, true
	case strings.HasPrefix(tok, "--project-dir="):
		return strings.TrimPrefix(tok, "--project-dir="), 0, true
	case strings.HasPrefix(tok, "-C") && len(tok) > 2:
		return strings.TrimPrefix(strings.TrimPrefix(tok, "-C"), "="), 0, true
	}
	return "", 0, false
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
