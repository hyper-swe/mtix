// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrLaunchRefused marks a launch plan or socket the harness will not use.
var ErrLaunchRefused = errors.New("launch refused")

// Launch describes one scratch session the driver is about to start.
type Launch struct {
	// Root is the absolute scratch root; everything the session may read
	// as configuration or use as a working directory lives under it.
	Root string
	// Cwd is the session's working directory.
	Cwd string
	// Args are the claude command-line arguments (without the program).
	Args []string
	// Env holds KEY=VALUE entries added to the session environment.
	Env []string
}

// CheckLaunch refuses a launch plan that could touch anything outside the
// scratch root or lower the session's permission checks: the working
// directory, every --settings file, every --add-dir, a CLAUDE_CONFIG_DIR
// and a messaging socket must resolve (through symlinks) inside Root; at
// least one file-based --settings is required; user-level settings must be
// kept out (CLAUDE_CONFIG_DIR inside Root, or --setting-sources without
// user); the dangerous-permission flags, the bypass permission mode and a
// settings file (or the project settings of Cwd) whose
// permissions.defaultMode is bypassPermissions are never allowed
// (MTIX-112.40.1; the bypass case is a lead hand-run, never launched by the
// harness).
func CheckLaunch(l Launch) error {
	root, err := resolveRoot(l.Root)
	if err != nil {
		return err
	}
	if err := requireInside(root, l.Cwd, "cwd"); err != nil {
		return err
	}
	if err := checkEnv(root, l.Env); err != nil {
		return err
	}
	if err := checkArgs(root, l.Args); err != nil {
		return err
	}
	flags := collectFlags(l.Args)
	if err := checkSettingContents(l.Cwd, flags.settings); err != nil {
		return err
	}
	return checkIsolation(root, flags, l.Env)
}

func resolveRoot(root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("%w: scratch root %q must be absolute", ErrLaunchRefused, root)
	}
	resolved, err := resolveLoose(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve scratch root: %w", ErrLaunchRefused, err)
	}
	if resolved == filepath.Dir(resolved) {
		return "", fmt.Errorf("%w: scratch root %q is the filesystem root", ErrLaunchRefused, root)
	}
	return resolved, nil
}

func checkEnv(root string, env []string) error {
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		switch k {
		case "CLAUDE_CONFIG_DIR":
			if err := requireInside(root, v, "config dir"); err != nil {
				return err
			}
		case envMessagingSocket:
			if err := requireInside(root, v, "messaging socket"); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkArgs(root string, args []string) error {
	settings := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case isPermissionFlag(a):
			return fmt.Errorf("%w: permission flag %s lowers permission checks", ErrLaunchRefused, a)
		case a == "--permission-mode" || strings.HasPrefix(a, "--permission-mode="):
			val, next := flagValue(args, i, "--permission-mode")
			i = next
			if strings.EqualFold(val, "bypassPermissions") {
				return fmt.Errorf("%w: permission mode %s lowers permission checks", ErrLaunchRefused, val)
			}
		case a == "--settings" || strings.HasPrefix(a, "--settings="):
			val, next := flagValue(args, i, "--settings")
			i = next
			if err := checkSettings(root, val); err != nil {
				return err
			}
			settings++
		}
	}
	if settings == 0 {
		return fmt.Errorf("%w: no --settings file inside the scratch root", ErrLaunchRefused)
	}
	return nil
}

// isPermissionFlag reports the flags that switch permission checks off.
func isPermissionFlag(a string) bool {
	for _, f := range []string{"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions"} {
		if a == f || strings.HasPrefix(a, f+"=") {
			return true
		}
	}
	return false
}

// flagValue returns the value of flag at args[i] in "--f v" or "--f=v" form
// and the index of the last argument consumed.
func flagValue(args []string, i int, flag string) (string, int) {
	if v, ok := strings.CutPrefix(args[i], flag+"="); ok {
		return v, i
	}
	if i+1 < len(args) {
		return args[i+1], i + 1
	}
	return "", i
}

func checkSettings(root, val string) error {
	if val == "" {
		return fmt.Errorf("%w: --settings has no value", ErrLaunchRefused)
	}
	if strings.HasPrefix(strings.TrimSpace(val), "{") {
		return fmt.Errorf("%w: --settings must be a file inside the scratch root, not inline JSON", ErrLaunchRefused)
	}
	return requireInside(root, val, "settings")
}

// requireInside refuses a path that is relative or resolves outside root.
func requireInside(root, p, what string) error {
	if !filepath.IsAbs(p) {
		return fmt.Errorf("%w: %s %q must be an absolute path", ErrLaunchRefused, what, p)
	}
	resolved, err := resolveLoose(p)
	if err != nil {
		return fmt.Errorf("%w: resolve %s: %w", ErrLaunchRefused, what, err)
	}
	if !within(root, resolved) {
		return fmt.Errorf("%w: %s %q is outside the scratch root", ErrLaunchRefused, what, p)
	}
	return nil
}

// within reports whether p is root or below it, by path components.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// resolveLoose resolves symlinks of the longest existing ancestor of p and
// re-appends the not-yet-existing tail, so a path to be created is judged
// by where it would really land.
func resolveLoose(p string) (string, error) {
	cur := filepath.Clean(p)
	var tail []string
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			parts := append([]string{resolved}, tail...)
			return filepath.Join(parts...), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("eval symlinks %s: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no existing ancestor of %s: %w", p, err)
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
}

// CheckSocket refuses a socket the driver does not own or that lies outside
// the scratch root. The socket is accepted only when the registry record of
// the driver's own child pid (the record passed in, read from that pid's
// registry file) names exactly this path, the record's pid is childPID, the
// record's cwd is inside the scratch root, the socket path itself resolves
// inside the scratch root (the driver sets CLAUDE_CODE_MESSAGING_SOCKET
// there), and the path is a unix socket.
func CheckSocket(root, path string, childPID int, rec Record) error {
	if !rec.Present["messagingSocketPath"] || !rec.Present["pid"] {
		return fmt.Errorf("%w: registry record lacks pid or messagingSocketPath", ErrLaunchRefused)
	}
	if rec.PID != childPID {
		return fmt.Errorf("%w: registry pid %d is not the child pid %d", ErrLaunchRefused, rec.PID, childPID)
	}
	if rec.SocketPath != path {
		return fmt.Errorf("%w: registry socket path differs from the socket %q", ErrLaunchRefused, path)
	}
	resolvedRoot, err := resolveRoot(root)
	if err != nil {
		return err
	}
	if cwdErr := requireInside(resolvedRoot, rec.Cwd, "registry cwd"); cwdErr != nil {
		return cwdErr
	}
	if sockErr := requireInside(resolvedRoot, path, "socket"); sockErr != nil {
		return sockErr
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: stat socket: %w", ErrLaunchRefused, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: %q is not a socket", ErrLaunchRefused, path)
	}
	return nil
}
