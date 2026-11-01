// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/service"
)

const (
	serveRecordIgnoreCheckID = "serve.record.ignore"
	serveRecordIgnoreEntry   = ".mtix/serve.json"
	serveRecordIgnoreFix     = "printf '\\n" + serveRecordIgnoreEntry + "\\n' >> .gitignore"
	gitCheckIgnoreTimeout    = 5 * time.Second
)

// registerServeRecordIgnoreCheck adds the serve.record.ignore row (MTIX-232).
// The row appears only inside a git working tree, like the other conditional
// rows, so projects without git see no new row. It writes nothing.
func registerServeRecordIgnoreCheck(svc *service.DoctorService) error {
	if app.mtixDir == "" {
		return nil
	}
	root := filepath.Dir(app.mtixDir)
	if !insideGitWorkTree(root) {
		return nil
	}
	return svc.Register(serveRecordIgnoreCheck(root))
}

// insideGitWorkTree reports whether dir or an ancestor holds a .git entry.
func insideGitWorkTree(dir string) bool {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// serveRecordIgnoreCheck warns when the project is a git repository that does
// not ignore .mtix/serve.json, so a git add of everything would commit the
// serve's pid and port. The fix is the exact line to run in the project root.
func serveRecordIgnoreCheck(root string) service.DoctorCheck {
	return service.DoctorCheck{ID: serveRecordIgnoreCheckID, Run: func(ctx context.Context) service.DoctorResult {
		ctx, cancel := context.WithTimeout(ctx, gitCheckIgnoreTimeout)
		defer cancel()
		// Constant arguments only; exit 0 = ignored, 1 = not ignored, 128 = not a repository.
		cmd := exec.CommandContext(ctx, "git", "-C", root, "check-ignore", "-q", "--", serveRecordIgnoreEntry) //nolint:gosec // fixed argument list, root is the project directory
		out, err := cmd.CombinedOutput()
		var exitErr *exec.ExitError
		switch {
		case err == nil:
			return service.DoctorResult{Status: service.DoctorPass, Detail: serveRecordIgnoreEntry + " is git-ignored"}
		case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
			return service.DoctorResult{
				Status: service.DoctorWarn,
				Detail: fmt.Sprintf("%s is not git-ignored: a git add of everything would commit the serve's pid and port", serveRecordIgnoreEntry),
				Fix:    serveRecordIgnoreFix,
			}
		case errors.As(err, &exitErr):
			// A .git entry exists yet git could not decide (for example it
			// refuses the repository): say so instead of reporting a pass.
			return service.DoctorResult{Status: service.DoctorInfo, Detail: fmt.Sprintf(
				"git could not check whether %s is ignored (exit %d): %s",
				serveRecordIgnoreEntry, exitErr.ExitCode(), format.SafeLine(firstLine(string(out))))}
		default:
			return service.DoctorResult{Status: service.DoctorInfo, Detail: "git is not available; cannot check whether " + serveRecordIgnoreEntry + " is ignored"}
		}
	}}
}

// firstLine returns the first non-empty line of s, or a placeholder.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return "no message from git"
}
