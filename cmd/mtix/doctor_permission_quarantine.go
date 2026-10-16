// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
)

func permissionQuarantineCheck(ctx context.Context, svc *service.WakePermissions, board service.PermissionBoard) service.DoctorResult {
	holds, err := svc.State.QuarantinedPermissionGrants(ctx)
	if err != nil {
		return permissionDoctorError(err)
	}
	var details, commands []string
	for _, hold := range holds {
		if hold.HostID == svc.HostID && hold.BoardID == board.ID {
			details = append(details, fmt.Sprintf("%s quarantine hold for legacy uncommitted batch %s at %s source %s", hold.AgentID, hold.QuarantineBatchID, hold.ConfirmedAt.UTC().Format(time.RFC3339Nano), hold.Source))
			commands = append(commands, "mtix agent allow "+hold.AgentID)
		}
	}
	if len(details) == 0 {
		return service.DoctorResult{Status: service.DoctorInfo, Detail: "no legacy permission quarantine holds"}
	}
	return service.DoctorResult{Status: service.DoctorWarn, Detail: strings.Join(details, "; "), Fix: strings.Join(commands, "; ")}
}
