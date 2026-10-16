// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// attachDoctorDeps feed the attach doctor checks (ADR-007 §7: setup.shim,
// glue.<h>, skew.dispatchers, and attach.failures for attach verb alerts).
type attachDoctorDeps struct {
	state       *oplocal.State
	registry    *attach.Registry
	current     int
	clock       func() time.Time
	dispatchers []service.DispatcherStatus
	// dispatchersErr is a heartbeat read failure, reported as a FAIL row.
	dispatchersErr error
	// unavailable explains why operator state cannot be read at all; then only
	// an INFO attach.state row is shown, with unavailableFix as its remedy.
	unavailable, unavailableFix string
}

// operatorStateFix is the remedy for unreadable operator state: the hooks
// status report names the concrete cause and validates a new placement.
const operatorStateFix = "mtix hooks status --json"

// failedRowCheck reports a detection failure as a row instead of failing doctor.
func failedRowCheck(id, detail, fix string) service.DoctorCheck {
	return service.DoctorCheck{ID: id, Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorFail, Detail: detail, Fix: fix}
	}}
}

// registerAttachDoctorChecks registers only the rows that apply: a host with
// no shim, no glue marks, no attach alerts and no dispatcher heartbeat gets
// none. Detection reads operator state once, here.
func registerAttachDoctorChecks(ctx context.Context, svc *service.DoctorService, deps attachDoctorDeps) error {
	if deps.unavailable != "" {
		return svc.Register(service.DoctorCheck{ID: "attach.state", Run: func(context.Context) service.DoctorResult {
			return service.DoctorResult{Status: service.DoctorInfo, Detail: "attach artifacts not checked: " + deps.unavailable, Fix: deps.unavailableFix}
		}})
	}
	var checks []service.DoctorCheck
	if shim := deps.registry.Writer("shim"); shim != nil {
		if status, err := shim.Check(ctx); err != nil || status.State != attach.ArtifactAbsent {
			checks = append(checks, shimDoctorCheck(shim))
		}
	}
	pending, err := attach.PendingGlueRefresh(deps.state)
	if err != nil {
		checks = append(checks, failedRowCheck("attach.state", "attach glue marks cannot be read: "+err.Error(), operatorStateFix))
	}
	harnesses := make([]string, 0, len(pending))
	for h := range pending {
		harnesses = append(harnesses, h)
	}
	sort.Strings(harnesses)
	for _, h := range harnesses {
		checks = append(checks, glueDoctorCheck(deps, h))
	}
	if failures := attachFailureAlerts(ctx, deps); len(failures) > 0 {
		checks = append(checks, attachFailuresCheck(deps))
	}
	if deps.dispatchersErr != nil {
		checks = append(checks, failedRowCheck("skew.dispatchers", "dispatcher heartbeats cannot be read: "+deps.dispatchersErr.Error(), "mtix verify"))
	} else if len(deps.dispatchers) > 0 {
		checks = append(checks, skewDispatchersCheck(deps.dispatchers, deps.current))
	}
	for _, check := range checks {
		if err := svc.Register(check); err != nil {
			return fmt.Errorf("register %s: %w", check.ID, err)
		}
	}
	return nil
}

func shimDoctorCheck(shim attach.Writer) service.DoctorCheck {
	return service.DoctorCheck{ID: "setup.shim", Run: func(ctx context.Context) service.DoctorResult {
		status, err := shim.Check(ctx)
		switch {
		case err != nil:
			return service.DoctorResult{Status: service.DoctorFail, Detail: "attach shim cannot be checked: " + err.Error(), Fix: operatorStateFix}
		case status.State == attach.ArtifactCurrent:
			return service.DoctorResult{Status: service.DoctorPass, Detail: status.Path + ": " + status.Detail}
		case status.State == attach.ArtifactStale:
			return service.DoctorResult{Status: service.DoctorWarn, Detail: status.Path + ": " + status.Detail, Fix: "mtix doctor --fix"}
		case status.State == attach.ArtifactAbsent:
			return service.DoctorResult{Status: service.DoctorInfo, Detail: "attach shim is not installed on this host"}
		default:
			return service.DoctorResult{Status: service.DoctorFail, Detail: status.Path + ": " + status.Detail, Fix: status.Fix}
		}
	}, Fixer: &service.DoctorFixer{Kind: service.DoctorRepairNonGrant, Apply: shim.Write}}
}

func glueDoctorCheck(deps attachDoctorDeps, harness string) service.DoctorCheck {
	check := service.DoctorCheck{ID: "glue." + harness, Run: func(context.Context) service.DoctorResult {
		return glueDoctorRow(deps, harness)
	}}
	if w := deps.registry.Writer("glue." + harness); w != nil {
		check.Fixer = &service.DoctorFixer{Kind: service.DoctorRepairNonGrant, Apply: func(ctx context.Context) error {
			_, err := deps.registry.Refresh(ctx, deps.state)
			return err
		}}
	}
	return check
}

func glueDoctorRow(deps attachDoctorDeps, harness string) service.DoctorResult {
	pending, err := attach.PendingGlueRefresh(deps.state)
	if err != nil {
		return service.DoctorResult{Status: service.DoctorFail, Detail: "attach glue marks cannot be read: " + err.Error(), Fix: operatorStateFix}
	}
	seen, marked := pending[harness]
	if !marked {
		return service.DoctorResult{Status: service.DoctorPass, Detail: fmt.Sprintf("%s glue is at attach protocol %d", harness, deps.current)}
	}
	writer := deps.registry.Writer("glue."+harness) != nil
	fix := glueSkewFix(harness, seen, deps.current, writer)
	switch {
	case seen > deps.current:
		return service.DoctorResult{Status: service.DoctorWarn,
			Detail: fmt.Sprintf("%s glue uses attach protocol %d, newer than this mtix (%d); its sessions are inbox-only", harness, seen, deps.current), Fix: fix}
	case writer:
		return service.DoctorResult{Status: service.DoctorWarn,
			Detail: fmt.Sprintf("%s glue uses attach protocol %d; this mtix rewrites it to %d", harness, seen, deps.current), Fix: fix}
	default:
		return service.DoctorResult{Status: service.DoctorWarn,
			Detail: fmt.Sprintf("%s glue uses attach protocol %d and this mtix build has no %s glue writer to refresh it; its sessions are inbox-only", harness, seen, harness), Fix: fix}
	}
}

// glueSkewFix is the one remedy for glue at another attach protocol, shared
// by the attach verb's line and alert and the glue.<harness> doctor row:
// newer glue needs a newer mtix; older glue is rewritten by mtix doctor --fix
// only when this build has a writer for it; otherwise the glue entries must
// be corrected or removed by hand.
func glueSkewFix(harness string, seen, current int, writer bool) string {
	switch {
	case seen > current:
		return fmt.Sprintf("upgrade mtix to the release that wrote the %s glue (compare with mtix version)", harness)
	case writer:
		return "mtix doctor --fix"
	default:
		return fmt.Sprintf("correct or remove the mtix attach entries in the %s configuration; this mtix build has no %s glue writer to refresh them", harness, harness)
	}
}

func attachFailureAlerts(ctx context.Context, deps attachDoctorDeps) []oplocal.Alert {
	rows, err := service.NewAlerts(deps.state, deps.clock).Recent(ctx, 0)
	if err != nil {
		return nil
	}
	var out []oplocal.Alert
	for _, row := range rows {
		if row.Class == attachAlertProtocolSkew || row.Class == attachAlertVerbFailure {
			out = append(out, row)
		}
	}
	return out
}

func attachFailuresCheck(deps attachDoctorDeps) service.DoctorCheck {
	return service.DoctorCheck{ID: "attach.failures", Run: func(ctx context.Context) service.DoctorResult {
		rows := attachFailureAlerts(ctx, deps)
		if len(rows) == 0 {
			return service.DoctorResult{Status: service.DoctorPass, Detail: "no recent attach failures"}
		}
		parts := make([]string, 0, len(rows))
		var fixes []string
		for _, row := range rows {
			parts = append(parts, fmt.Sprintf("%s %s count %d at %s: %s", row.Class, row.Agent, row.Count, row.LastAt.UTC().Format(time.RFC3339), row.Detail))
			if row.Fix != "" && !containsString(fixes, row.Fix) {
				fixes = append(fixes, row.Fix)
			}
		}
		return service.DoctorResult{Status: service.DoctorWarn, Detail: strings.Join(parts, "; "), Fix: strings.Join(fixes, "; ")}
	}}
}

func skewDispatchersCheck(statuses []service.DispatcherStatus, current int) service.DoctorCheck {
	return service.DoctorCheck{ID: "skew.dispatchers", Run: func(context.Context) service.DoctorResult {
		var details, pids []string
		for _, d := range statuses {
			if !d.NeedsAttention() {
				continue
			}
			pids = append(pids, fmt.Sprint(d.PID))
			if d.BelowFloor {
				details = append(details, fmt.Sprintf("dispatcher PID %d (mtix %s, attach protocol %d) is below this board's min_dispatch_protocol %d and performs no wake pass", d.PID, d.Version, d.Protocol, d.Floor))
				continue
			}
			details = append(details, fmt.Sprintf("dispatcher PID %d (mtix %s, attach protocol %d) is older than this mtix (%d)", d.PID, d.Version, d.Protocol, current))
		}
		if len(details) == 0 {
			return service.DoctorResult{Status: service.DoctorPass, Detail: fmt.Sprintf("%d dispatcher(s) at a current attach protocol", len(statuses))}
		}
		return service.DoctorResult{Status: service.DoctorWarn,
			Detail: strings.Join(details, "; ") + "; no dispatcher service is installed by this mtix, so it cannot restart them",
			Fix:    "stop and start the process with PID " + strings.Join(pids, ", PID ") + " so it runs the current mtix"}
	}}
}

// productionAttachDoctorDeps reads this host's operator state and, inside a
// project, the board's dispatcher heartbeats for this host and user.
// It never fails doctor over operator state: without a usable operator
// directory nothing attach-related can be installed, so it reports one INFO
// row with the remedy instead (the base doctor exit codes are kept).
func productionAttachDoctorDeps(ctx context.Context) (attachDoctorDeps, error) {
	st, err := operatorState()
	if err != nil {
		return attachDoctorDeps{unavailable: err.Error(), unavailableFix: "set HOME (or XDG_CONFIG_HOME) to the operator's home directory, then run mtix hooks status --json"}, nil
	}
	if _, inspectErr := st.Inspect(); inspectErr != nil {
		return attachDoctorDeps{unavailable: inspectErr.Error(), unavailableFix: operatorStateFix}, nil
	}
	reg, err := productionAttachRegistry(st, attach.Protocol)
	if err != nil {
		return attachDoctorDeps{}, err
	}
	deps := attachDoctorDeps{state: st, registry: reg, current: attach.Protocol, clock: func() time.Time { return time.Now().UTC() }}
	if app.store == nil {
		return deps, nil
	}
	// A heartbeat is keyed by this host's id; with no operator directory there
	// is no host id, so no heartbeat can be ours. Never create state here.
	if dir, pathErr := st.Path(); pathErr != nil || !pathExists(dir) {
		return deps, nil //nolint:nilerr // an unusable operator directory is reported by the shim and alert rows
	}
	host, err := existingRegistrationDoctorHost()
	if err != nil || host.HostID == "" {
		return deps, nil //nolint:nilerr // without a host identity no heartbeat can be ours; the registration checks report the identity gap
	}
	deps.dispatchers, deps.dispatchersErr = service.NewDispatcherSkew(app.store, host.HostID, host.UID, attach.Protocol).Statuses(ctx)
	return deps, nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func containsString(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}
