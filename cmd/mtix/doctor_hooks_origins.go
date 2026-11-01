// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

const hooksOriginsCheckID = "hooks.origins"

// hooksOriginsCheck lists configured webhook origins that delivery would
// refuse (not consented, or private without the explicit opt-in), each with the
// exact fix (FR-19, W15). It only reads operator state and writes nothing. The
// row is registered only when hooks.yaml exists.
func hooksOriginsCheck(mtixDir string) service.DoctorCheck {
	return service.DoctorCheck{ID: hooksOriginsCheckID, Run: func(context.Context) service.DoctorResult {
		cfg, _ := hooks.Load(mtixDir)
		var list hooks.OriginList
		env, err := oplocal.FromProcess()
		if err == nil {
			list, err = hooks.ReadOrigins(oplocal.New(env))
		}
		return originsResult(hooks.WebhookOriginIssues(list, err, cfg))
	}}
}

func originsResult(issues []hooks.OriginIssue) service.DoctorResult {
	if len(issues) == 0 {
		return service.DoctorResult{Status: service.DoctorPass, Detail: "every configured webhook origin is consented"}
	}
	details := make([]string, 0, len(issues))
	fixes := make([]string, 0, len(issues))
	for _, issue := range issues {
		details = append(details, fmt.Sprintf("%s (%s)", format.SafeLine(issue.Hook), format.SafeLine(issue.Reason)))
		if fix := format.SafeLine(issue.Fix); !slices.Contains(fixes, fix) {
			fixes = append(fixes, fix)
		}
	}
	return service.DoctorResult{
		Status: service.DoctorWarn,
		Detail: "webhook hooks that would be skipped: " + strings.Join(details, "; "),
		Fix:    strings.Join(fixes, "; "),
	}
}
