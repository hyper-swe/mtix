// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/service"
)

const hooksProgramsCheckID = "hooks.programs"

// hooksFileExists reports whether hooks.yaml is present; without it the check
// has nothing to say and registers no row.
func hooksFileExists(mtixDir string) bool {
	_, err := os.Stat(filepath.Join(mtixDir, "hooks.yaml"))
	return err == nil
}

// hooksProgramsCheck lists approved exec hooks whose program is neither a
// pinned project script nor a known interpreter running one, hooks that could
// not be classified, and hooks.yaml load problems. The row is advisory (WARN)
// and never changes how a hook launches.
func hooksProgramsCheck(mtixDir string) service.DoctorCheck {
	return service.DoctorCheck{ID: hooksProgramsCheckID, Run: func(context.Context) service.DoctorResult {
		_, loadWarnings := hooks.Load(mtixDir)
		approvals, err := hooks.ProcessApprovals(mtixDir)
		if err != nil {
			return programsResult(nil, loadWarnings, err)
		}
		decisions, err := approvals.Check()
		return programsResult(decisions, loadWarnings, err)
	}}
}

// programsResult builds the hooks.programs row. A trusted hook with no review
// counts as unclassified: the check fails closed.
func programsResult(decisions map[string]hooks.Decision, loadWarnings []string, err error) service.DoctorResult {
	if err != nil && len(decisions) == 0 {
		return programsUnavailable(err)
	}
	var names []string
	for name, decision := range decisions {
		if decision.Trusted && (decision.ProgramReview == nil || decision.ProgramReview.Needed) {
			names = append(names, name)
		}
	}
	if len(names) == 0 && len(loadWarnings) == 0 {
		return service.DoctorResult{Status: service.DoctorPass, Detail: "no approved exec hook needs program review"}
	}
	sort.Strings(names)
	var details, fixes []string
	if len(names) > 0 {
		trust := make([]string, len(names))
		for i, name := range names {
			trust[i] = hooks.TrustFix(name)
		}
		details = append(details, "approved exec hooks whose program is not a pinned project script or a known interpreter running one, or that could not be classified: "+strings.Join(names, ", "))
		fixes = append(fixes, "For each hook, run the program through a pinned project script (a script file in the project that calls it), then approve it again: "+strings.Join(trust, "; "))
	}
	if len(loadWarnings) > 0 {
		details = append(details, "hooks.yaml problems: "+strings.Join(loadWarnings, "; "))
		fixes = append(fixes, "edit .mtix/hooks.yaml, then run mtix hooks status --json")
	}
	return service.DoctorResult{Status: service.DoctorWarn, Detail: strings.Join(details, ". "), Fix: strings.Join(fixes, ". ")}
}

func programsUnavailable(err error) service.DoctorResult {
	return service.DoctorResult{
		Status: service.DoctorWarn,
		Detail: fmt.Sprintf("hook program review could not be evaluated: %v", err),
		Fix:    "mtix hooks status --json",
	}
}

const hooksStrictCheckID = "hooks.strict"

// hooksStrictCheck reports strict program mode. It passes when the mode is off
// or every hook program is accounted for, and warns, with the exact approval
// commands, when the mode is on and a hook program is not approved (such a
// hook is refused at launch).
func hooksStrictCheck(mtixDir string) service.DoctorCheck {
	return service.DoctorCheck{ID: hooksStrictCheckID, Run: func(context.Context) service.DoctorResult {
		approvals, err := hooks.ProcessApprovals(mtixDir)
		if err != nil {
			return programsUnavailable(err)
		}
		cfg, _ := hooks.Load(mtixDir)
		st := approvals.StrictStatus(cfg)
		if !st.Enabled && st.Fix != "" {
			// An unreadable strict record refuses every exec launch.
			return service.DoctorResult{Status: service.DoctorFail, Detail: st.Detail, Fix: st.Fix}
		}
		if st.Fix != "" {
			return service.DoctorResult{Status: service.DoctorWarn, Detail: st.Detail, Fix: st.Fix}
		}
		return service.DoctorResult{Status: service.DoctorPass, Detail: st.Detail}
	}}
}
