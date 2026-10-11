// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
)

func pendingExportsLabel(status service.PendingExportStatus) string {
	if status.Count == 0 {
		return "pending exports: 0, oldest -"
	}
	return fmt.Sprintf("pending exports: %d, oldest %s", status.Count, time.Duration(status.OldestAgeSeconds)*time.Second)
}

func checkPendingExports(ctx context.Context) DoctorCheck {
	check := DoctorCheck{Name: "pending exports"}
	if app.syncSvc == nil {
		check.Detail = "local sync service not initialized"
		return check
	}
	status, err := app.syncSvc.PendingExports(ctx, app.mtixDir)
	if err != nil {
		check.Detail = fmt.Sprintf("read pending exports: %v", err)
		return check
	}
	check.Pass, check.PendingExports = true, &status
	check.Detail = pendingExportsLabel(status)
	if status.Count > 0 && status.Age > time.Hour {
		check.Warn = true
		check.Detail += "; protected mirror recovery may be stuck; a pulled-board refusal still needs its printed resolution"
		check.Fix = "mtix list"
	}
	return check
}
