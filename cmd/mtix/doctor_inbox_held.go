// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/service"
)

// inboxHeldCheck reports registered ids whose inbox holds unacknowledged events
// the wake policy withholds from every agent-facing read (ADR-007 A12). The
// fix is the exact operator command; the machine-readable Data lists the held
// count per id.
func inboxHeldCheck(ctx context.Context, perms *service.WakePermissions, board service.PermissionBoard) service.DoctorResult {
	rows, err := app.store.ListRegistrations(ctx, perms.HostID, perms.UID)
	if err != nil {
		return permissionDoctorError(err)
	}
	inbox := service.NewInboxService(app.store)
	counts := map[string]int64{}
	var ids []string
	for _, row := range rows {
		ok, reason, wakeErr := perms.Wakeable(ctx, board, row.AgentID)
		if wakeErr != nil {
			return permissionDoctorError(wakeErr)
		}
		if ok || reason == "denied" {
			continue
		}
		pending, _, countErr := inbox.InboxCount(ctx, row.AgentID)
		if countErr != nil {
			return permissionDoctorError(countErr)
		}
		if pending > 0 {
			counts[row.AgentID] = pending
			ids = append(ids, row.AgentID)
		}
	}
	if len(ids) == 0 {
		return service.DoctorResult{Status: service.DoctorPass, Detail: "no registered id has held inbox events"}
	}
	sort.Strings(ids)
	parts := make([]string, len(ids))
	fixes := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%s (%d held)", id, counts[id])
		fixes[i] = "mtix agent allow " + id
	}
	return service.DoctorResult{
		Status: service.DoctorFail,
		Detail: "inbox events held by the wake policy: " + strings.Join(parts, ", "),
		Fix:    strings.Join(fixes, "; "),
		Data:   map[string]any{"held": counts},
	}
}
