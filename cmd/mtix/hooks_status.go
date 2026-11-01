// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/spf13/cobra"
)

func newHooksStatusCmd() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "Show operator-local state and per-hook approvals", Long: "Validate operator-local state and report its path, availability, per-hook approvals, legacy workspace records, and exact corrective commands. This command does not create state or grant hook approval.", Args: cobra.NoArgs, RunE: func(_ *cobra.Command, _ []string) error {
		env, err := oplocal.FromProcess()
		if err != nil {
			return err
		}
		return runHooksStatus(oplocal.New(env))
	}}
}

type hookStatusReport struct {
	oplocal.Status
	Hooks    []hooks.Decision `json:"hooks"`
	Legacy   string           `json:"legacy,omitempty"`
	Warnings []string         `json:"warnings"`
	// AppendFile lists append-file paths delivery would refuse or that are too open.
	AppendFile []hooks.AppendFileIssue `json:"append_file"`
	// Strict reports strict program mode and the program approvals.
	Strict hooks.StrictStatus `json:"strict"`
	// Origins lists webhook hooks whose delivery origin consent would refuse (W15).
	Origins []hooks.OriginIssue `json:"origins"`
}

func runHooksStatus(state *oplocal.State) error {
	report, err := inspectHookStatus(state)
	out := NewOutputWriter(app.jsonOutput)
	if app.jsonOutput {
		if outputErr := out.WriteJSON(report); outputErr != nil {
			return fmt.Errorf("write status: %w", outputErr)
		}
	} else {
		renderHookStatus(out, report)
	}
	return err
}

func inspectHookStatus(state *oplocal.State) (hookStatusReport, error) {
	status, err := state.Inspect()
	if err != nil {
		status.Status = "refused"
		status.Detail = err.Error()
		status.Fix = "Choose an operator-owned configuration directory outside projects, temporary directories and harness writable roots; remove symlink components and use directory mode 0700 and file mode 0600 (a user-only ACL on Windows), set XDG_CONFIG_HOME on Unix to a directory whose ancestors are owned by root or the operator and have no group or other write access (group-writable homes and unsupported network or directory-service-owned parents are refused), or choose APPDATA with supported ancestor access on Windows; then run mtix hooks status --json. Never commit or dotfile-sync this directory."
	}
	approvals := hooks.NewApprovals(app.mtixDir, state, hooks.SessionMarked())
	decisions, hookErr := approvals.Check()
	names := make([]string, 0, len(decisions))
	for name := range decisions {
		names = append(names, name)
	}
	sort.Strings(names)
	entries := make([]hooks.Decision, 0, len(names))
	for _, name := range names {
		decision := decisions[name]
		entries = append(entries, decision)
	}
	cfg, warnings := hooks.Load(app.mtixDir)
	appendIssues := appendFileIssues(cfg)
	origins, originsErr := hooks.ReadOrigins(state)
	originIssues := hooks.WebhookOriginIssues(origins, originsErr, cfg)
	if warnings == nil {
		warnings = []string{}
	}
	strict := approvals.StrictStatus(cfg)
	return hookStatusReport{Status: status, Hooks: entries, Legacy: hooks.LegacyStatus(app.mtixDir), Warnings: warnings, AppendFile: appendIssues, Strict: strict, Origins: originIssues}, errors.Join(err, hookErr)
}

func renderHookStatus(out OutputWriter, report hookStatusReport) {
	out.WriteHuman("Operator-local state: %s\nPath: %s\n", report.Status.Status, report.Path)
	for _, limit := range report.Limitations {
		out.WriteHuman("Limit: %s\n", limit)
	}
	if report.Detail != "" {
		out.WriteHuman("%s\nFix: %s\n", report.Detail, report.Fix)
	}
	for _, decision := range report.Hooks {
		out.WriteHuman("%s: trusted=%t (%s)\nFix: %s\n", decision.Name, decision.Trusted, decision.Detail, decision.Fix)
		if review := decision.ProgramReview; review != nil && review.Needed {
			out.WriteHuman("%s: program review needed (%s)\nFix: %s\n", decision.Name, review.Detail, review.Fix)
		}
	}
	renderStrictStatus(out, report.Strict)
	if report.Legacy != "" {
		out.WriteHuman("%s\n", report.Legacy)
	}
	for _, item := range report.Reports {
		out.WriteHuman("%s: %s\n", item.Name, item.Reason)
	}
	for _, issue := range report.AppendFile {
		out.WriteHuman("hook %q append-file: %s\nFix: %s\n", issue.Hook, issue.Reason, issue.Fix)
	}
	for _, issue := range report.Origins {
		out.WriteHuman("hook %q webhook: %s\nFix: %s\n", issue.Hook, issue.Reason, issue.Fix)
	}
	for _, warning := range report.Warnings {
		out.WriteHuman("%s\nFix: edit .mtix/hooks.yaml, then run mtix hooks status --json\n", warning)
	}
}

// appendFileIssues reports each configured append-file path delivery would
// refuse, or that is wider than owner-only (TM I34), with the exact fix.
func appendFileIssues(cfg hooks.Config) []hooks.AppendFileIssue {
	base := filepath.Dir(app.mtixDir)
	out := []hooks.AppendFileIssue{}
	for _, h := range cfg.Hooks {
		out = append(out, hooks.AppendFileIssues(base, h)...)
	}
	return out
}
