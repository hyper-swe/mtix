// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func registerPermissionDoctor(svc *service.DoctorService) error {
	config := readPermissionDoctorContext()
	if !config.relevant {
		return nil
	}
	checks := []service.DoctorCheck{
		{ID: "agents.unconfirmed", Run: func(ctx context.Context) service.DoctorResult {
			if config.err != nil {
				return permissionDoctorError(config.err)
			}
			return permissionUnconfirmedCheck(ctx, config.permissions, config.board)
		}},
		{ID: "agents.confirmations", Run: func(ctx context.Context) service.DoctorResult {
			if config.err != nil {
				return permissionDoctorError(config.err)
			}
			return permissionConfirmationsCheck(ctx, config.permissions, config.board)
		}},
	}
	if config.err == nil {
		checks = append(checks, service.DoctorCheck{ID: "agents.quarantined", Run: func(ctx context.Context) service.DoctorResult {
			return permissionQuarantineCheck(ctx, config.permissions, config.board)
		}})
		checks = append(checks, service.DoctorCheck{ID: "agents.denied", Run: func(ctx context.Context) service.DoctorResult {
			return permissionDeniedCheck(ctx, config.permissions, config.board)
		}})
	}
	for _, check := range checks {
		if err := svc.Register(check); err != nil {
			return err
		}
	}
	return nil
}

func permissionUnconfirmedCheck(ctx context.Context, svc *service.WakePermissions, board service.PermissionBoard) service.DoctorResult {
	result := service.DoctorResult{Status: service.DoctorPass, Detail: "no unconfirmed registered ids"}
	rows, err := app.store.ListRegistrations(ctx, svc.HostID, svc.UID)
	if err != nil {
		return permissionDoctorError(err)
	}
	var held []string
	for _, row := range rows {
		ok, reason, err := svc.Wakeable(ctx, board, row.AgentID)
		if err != nil {
			return permissionDoctorError(err)
		}
		if !ok && reason != "denied" {
			held = append(held, row.AgentID+" ("+reason+")")
			if result.Fix == "" {
				result.Fix = "mtix agent allow " + row.AgentID
			}
		}
	}
	if len(held) != 0 {
		result.Status, result.Detail = service.DoctorFail, "held ids: "+strings.Join(held, ", ")
	}
	return result
}

func permissionConfirmationsCheck(ctx context.Context, svc *service.WakePermissions, board service.PermissionBoard) service.DoctorResult {
	rows, err := svc.State.Confirmations(ctx)
	if err != nil {
		return permissionDoctorError(err)
	}
	var lines []string
	for _, row := range rows {
		if row.BoardID != board.ID {
			continue
		}
		lines = append(lines, permissionConfirmationDetail(row))
	}
	pending, err := svc.PendingRegistrations(ctx, board)
	if err != nil {
		return permissionDoctorError(err)
	}
	usage, err := svc.State.PermissionReplayUsage(ctx)
	if err != nil {
		return permissionDoctorError(err)
	}
	if usage.Count != 0 {
		lines = append(lines, fmt.Sprintf("file replay receipts: %d/%d ids, %d/%d bytes; oldest completed receipts are evicted at capacity; pending receipts are retained", usage.Count, usage.MaxCount, usage.Bytes, usage.MaxBytes))
	}
	lines = append(lines, fmt.Sprintf("pending registration count: %d", len(pending)))
	denied := permissionDeniedFileOutcomes(rows, board.ID)
	commands := permissionRecoveryCommands(rows, board.ID)
	lines = append(lines, fmt.Sprintf("retained denied file allow count: %d", denied))
	for _, agent := range pending {
		commands = append(commands, "mtix agent deny "+agent)
	}
	return service.DoctorResult{Status: service.DoctorInfo, Detail: strings.Join(lines, "; "), Fix: strings.Join(commands, "; ")}
}

func permissionConfirmationDetail(row oplocal.Confirmation) string {
	line := fmt.Sprintf("%s agent %s action %s mode %s source %s board_line %s", row.Time.UTC().Format(time.RFC3339Nano), row.AgentID, row.Action, row.Mode, row.Source, row.BoardLine)
	if row.Pending {
		line += "; board publication pending; inspect with mtix agent permissions recover"
	}
	if len(row.Markers) != 0 {
		line += "; harness session markers: " + strings.Join(row.Markers, ", ")
	}
	if row.Outcome != "" {
		line += "; outcome " + row.Outcome
	}
	return line
}

func permissionDeniedFileOutcomes(rows []oplocal.Confirmation, board string) int {
	var count int
	for _, row := range rows {
		if row.BoardID == board && row.Outcome == "denied_requires_operator" {
			count++
		}
	}
	return count
}

func permissionDeniedCheck(_ context.Context, svc *service.WakePermissions, board service.PermissionBoard) service.DoctorResult {
	grants, err := svc.State.AgentGrants()
	if err != nil {
		return permissionDoctorError(err)
	}
	var denied []string
	for _, grant := range grants {
		if grant.HostID == svc.HostID && grant.BoardID == board.ID && grant.Denied {
			denied = append(denied, fmt.Sprintf("%s denied by operator at %s source %s", grant.AgentID, grant.ConfirmedAt.UTC().Format(time.RFC3339Nano), grant.Source))
		}
	}
	if len(denied) == 0 {
		return service.DoctorResult{Status: service.DoctorInfo, Detail: "no operator denials on this board"}
	}
	return service.DoctorResult{Status: service.DoctorInfo, Detail: strings.Join(denied, "; ")}
}

func permissionRecoveryCommands(rows []oplocal.Confirmation, board string) []string {
	for _, row := range rows {
		if row.BoardID == board && row.Pending {
			return []string{"mtix agent permissions recover"}
		}
	}
	return nil
}
