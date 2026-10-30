// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/service"
)

const daemonLogWarningBytes = 10 * 1024 * 1024

const daemonLogRetentionFix = "Preserve needed records, then configure the platform retention recipe in USERMANUAL.md: " +
	"on macOS use sudo /usr/sbin/newsyslog -f /etc/newsyslog.d/mtix-daemon.conf; " +
	"where logrotate is installed use sudo logrotate /etc/logrotate.d/mtix-daemon. " +
	"These commands require an administrator-installed configuration first; doctor --fix does not rotate logs."

// registerDaemonLogsCheck inspects only the two supervisor capture files. It
// deliberately has no fixer: even --fix must leave diagnostic records intact.
func registerDaemonLogsCheck(svc *service.DoctorService) error {
	if app.mtixDir == "" {
		return nil
	}
	row := service.DoctorResult{Status: service.DoctorPass}
	var details, remedies []string
	for _, name := range []string{"daemon.out.log", "daemon.err.log"} {
		result, present := inspectDaemonLog(filepath.Join(app.mtixDir, "logs", name), name)
		if !present {
			continue
		}
		details = append(details, result.Detail)
		if result.Status == service.DoctorWarn {
			row.Status = service.DoctorWarn
			if len(remedies) == 0 || remedies[len(remedies)-1] != result.Fix {
				remedies = append(remedies, result.Fix)
			}
		}
	}
	if len(details) == 0 {
		return nil
	}
	row.Detail, row.Fix = strings.Join(details, "; "), strings.Join(remedies, " ")
	return svc.Register(service.DoctorCheck{ID: "daemon.logs", Run: func(context.Context) service.DoctorResult { return row }})
}

// inspectDaemonLog reads metadata, never contents, and refuses to treat a
// symlink or another nonregular file as an ordinary rotation target.
func inspectDaemonLog(path, name string) (service.DoctorResult, bool) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return service.DoctorResult{}, false
	}
	row := service.DoctorResult{Status: service.DoctorWarn,
		Fix: "Inspect .mtix/logs ownership, permissions and file types manually before configuring retention; doctor --fix leaves these paths unchanged."}
	if err != nil {
		row.Detail = fmt.Sprintf("cannot inspect .mtix/logs/%s: %v", name, err)
		return row, true
	}
	if !info.Mode().IsRegular() {
		row.Detail = fmt.Sprintf(".mtix/logs/%s is not a regular file", name)
		return row, true
	}
	row.Detail = fmt.Sprintf(".mtix/logs/%s: %d bytes (warning above %d bytes / 10 MiB)", name, info.Size(), daemonLogWarningBytes)
	if info.Size() > daemonLogWarningBytes {
		row.Fix = daemonLogRetentionFix
	} else {
		row.Status, row.Fix = service.DoctorPass, ""
	}
	return row, true
}
