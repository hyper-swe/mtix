// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// errHardenPending is returned by runSyncHarden after the report is printed
// when changes are pending (dry run) or access remains (--apply). It maps
// to exit code 2, distinct from an error or a refusal (1) (MTIX-95.1).
var errHardenPending = errors.New("harden: changes are pending or access remains; see the report")

// verificationPassed states exactly what a passing verification checked
// (MTIX-95.1): superusers, and access through a kept role, are out of its
// scope.
const verificationPassed = "verification passed: apart from the table owner, superusers, and the kept roles " +
	"and their members,\n  no role holds a privilege on the sync tables, their sequences or the mtix " +
	"functions (EXECUTE on the\n  trigger functions aside), or a role membership, ADMIN OPTION included, " +
	"that leads to one;\n  the owner's default privileges give such roles nothing; every TRUNCATE guard is in place."

// hardenFlags are the flags of `mtix sync harden`.
type hardenFlags struct {
	apply     bool
	keepRoles []string
}

// newSyncHardenCmd creates `mtix sync harden`, the owner's command that
// removes access to the sync tables from every role that should not have
// it (MTIX-95.1). It is a dry run unless --apply is given, and runs only on
// explicit request, never from push, pull or the daemon.
func newSyncHardenCmd() *cobra.Command {
	var flags hardenFlags
	var insecureTLS bool
	cmd := &cobra.Command{
		Use:   "harden",
		Short: "Owner only: restrict the hub's sync tables to the owner and the roles you keep",
		Long: `Check which roles can use the hub's sync tables, sequences and mtix
functions, and with --apply restrict them to the owner and the roles you
keep. Without --apply this is a dry run: it lists every role, default
privilege and membership it would change, and changes nothing.

With --apply, in one transaction, it revokes every privilege on those
objects, column privileges included, from PUBLIC, from the roles a data
API uses for anonymous and signed-in callers, and from every other role
except the table owner, superusers and the roles named with --keep-role
or in the sync.keep_roles config key. A kept role keeps its privileges,
and its members keep them through it, but it loses any right to grant
them on; the owner first grants again anything a kept role holds by
another role's grant. The owner's default privileges that would give
those roles access to tables created later are revoked too. A membership
in pg_read_all_data, pg_write_all_data or pg_maintain, even one with only
ADMIN OPTION, is revoked when the owner may do so; it is cluster-wide. A missing TRUNCATE guard is restored and a
disabled one enabled. A server WARNING fails the run and nothing changes.
Access it cannot remove is reported with the statement an administrator
runs. EXECUTE on the mtix trigger functions and other roles' default
privileges are information and never fail verification.

Run it as the role that owns the sync tables, or as a superuser or a
member of the owner role; any other role is refused and nothing changes.
Superusers are not checked. Review the dry run's role list before
--apply: a role you do not keep loses its access.

Exit code: 0 when verification passes, 2 when changes are pending (dry
run) or access remains (--apply), 1 on an error or a refusal. --json
prints the report for agents and CI.`,
		Args: syncExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSyncHarden(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.apply, "apply", false,
		"Make the changes the dry run lists (without it nothing is changed)")
	cmd.Flags().StringArrayVar(&flags.keepRoles, "keep-role", nil,
		"A role that keeps its access to the sync tables (repeatable; adds to sync.keep_roles)")
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes only when every host the connection may use is loopback or a local socket (development only)")
	return cmd
}

// runSyncHarden verifies the hub's privileges and, with --apply, restricts
// them (MTIX-95.1). The kept roles are --keep-role unioned with
// sync.keep_roles; both are validated before any connection is made. The
// pool records server WARNINGs so a REVOKE that PostgreSQL could not carry
// out fails the run.
func runSyncHarden(ctx context.Context, stdout, _ io.Writer,
	args []string, opts transport.Options, flags hardenFlags,
) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync harden: not in an mtix project (run 'mtix init' first)")
	}
	configured := ""
	if app.configSvc != nil {
		v, err := app.configSvc.Get("sync.keep_roles")
		if err != nil {
			return hardenErr("config", err)
		}
		configured = v
	}
	kept, hint, err := keptRoles(flags.keepRoles, configured)
	if err != nil {
		return hardenErr("kept roles", err)
	}
	dsn, err := resolveSyncDSN(args)
	if err != nil {
		return hardenErr("dsn", err)
	}
	warnings := transport.NewWarningLog()
	opts.OnNotice = warnings.Record
	pool, err := transport.New(ctx, dsn, opts)
	if err != nil {
		return hardenErr("connect", err)
	}
	defer pool.Close()

	result, err := pool.Harden(ctx, transport.HardenRequest{
		Apply: flags.apply, KeepRoles: kept, Warnings: warnings,
	})
	if err != nil {
		return hardenErr("", err)
	}
	if err := printHardenResult(stdout, result, flags.apply, hint); err != nil {
		return hardenErr("report", err)
	}
	return hardenOutcome(result)
}

// hardenSentinels are the errors a harden failure keeps in its chain after
// the DSN scrub, so callers can test them with errors.Is. None of them
// carries a DSN.
var hardenSentinels = []error{
	transport.ErrHardenNotOwner, transport.ErrHubWarning,
	transport.ErrSyncSchemaIncomplete, model.ErrInvalidInput,
}

// hardenError is a scrubbed harden failure that still unwraps to the
// sentinel it came from.
type hardenError struct {
	msg      string
	sentinel error
}

// Error returns the scrubbed message.
func (e *hardenError) Error() string { return e.msg }

// Unwrap returns the sentinel, or nil.
func (e *hardenError) Unwrap() error { return e.sentinel }

// hardenErr formats a harden error through the central DSN scrubber and
// keeps a known sentinel in its chain, as wrapSyncErr does (MTIX-95.1).
// Unlike wrapSyncErr it ignores hook mode: harden is never run from a
// hook, and its failures must never be downgraded to a warning. An empty
// stage is for errors from Pool.Harden, whose text starts with "harden:".
func hardenErr(stage string, err error) error {
	prefix := "mtix sync harden " + stage + ": "
	if stage == "" {
		prefix = "mtix sync "
	}
	out := &hardenError{msg: prefix + scrubSyncText(err.Error())}
	for _, sentinel := range hardenSentinels {
		if errors.Is(err, sentinel) {
			out.sentinel = sentinel
			break
		}
	}
	return out
}

// hardenOutcome maps the final verification to the exit contract: nil when
// it passed, errHardenPending when changes are pending or access remains.
func hardenOutcome(r *transport.HardenResult) error {
	final := r.Before
	if r.After != nil {
		final = r.After
	}
	if final.Clean() {
		return nil
	}
	return errHardenPending
}

// keptRoles returns the kept roles, the union of --keep-role and the
// sync.keep_roles value, and, when the flags name a role the config lacks,
// the exact command that records the union. Config is never written here.
func keptRoles(flags []string, configured string) ([]string, string, error) {
	fromFlags, err := model.ValidateKeepRoles(flags)
	if err != nil {
		return nil, "", fmt.Errorf("--keep-role: %w", err)
	}
	fromConfig, err := model.ParseKeepRoles(configured)
	if err != nil {
		return nil, "", fmt.Errorf("sync.keep_roles in .mtix/config.yaml: %w", err)
	}
	all, err := model.ValidateKeepRoles(append(fromConfig, fromFlags...))
	if err != nil {
		return nil, "", fmt.Errorf("kept roles: %w", err)
	}
	inConfig := map[string]bool{}
	for _, r := range fromConfig {
		inConfig[r] = true
	}
	for _, r := range fromFlags {
		if !inConfig[r] {
			return all, "mtix config set sync.keep_roles " + strings.Join(all, ","), nil
		}
	}
	return all, "", nil
}

// hardenJSON is the --json form of the result, with the config hint.
type hardenJSON struct {
	*transport.HardenResult
	KeepRolesHint string `json:"keep_roles_hint,omitempty"`
}

// printHardenResult writes the report as JSON or as text.
func printHardenResult(w io.Writer, r *transport.HardenResult, apply bool, hint string) error {
	if app.jsonOutput {
		body, err := json.MarshalIndent(hardenJSON{HardenResult: r, KeepRolesHint: hint}, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal report: %w", err)
		}
		fmt.Fprintln(w, string(body))
		return nil
	}
	if !apply {
		printHardenDryRun(w, r.Before)
		return nil
	}
	printHardenApply(w, r, hint)
	return nil
}

// printHardenDryRun writes the dry run: what --apply would change, what it
// cannot, and the statements it would run.
func printHardenDryRun(w io.Writer, rep *transport.PrivilegeReport) {
	fmt.Fprintln(w, "mtix sync harden: dry run; nothing was changed.")
	printHardenScope(w, rep)
	if rep.Clean() {
		fmt.Fprintln(w, verificationPassed)
		printHardenInfo(w, rep)
		return
	}
	printHardenRoles(w, rep)
	printHardenFindings(w, "Changes --apply would make:", rep, true)
	printHardenFindings(w, "Access --apply cannot remove (an administrator must act):", rep, false)
	printHardenInfo(w, rep)
	if len(rep.Statements) > 0 {
		fmt.Fprintln(w, "\nStatements --apply would run, in order:")
		for _, s := range rep.Statements {
			fmt.Fprintln(w, "  "+safeText(s))
		}
		fmt.Fprintln(w, "\nReview every role above: a role you do not keep loses its access.")
		fmt.Fprintln(w, "Then run: mtix sync harden --apply"+keepFlags(rep.KeptRoles))
	}
}

// printHardenApply writes what --apply ran and the verification after it.
func printHardenApply(w io.Writer, r *transport.HardenResult, hint string) {
	if !r.Applied {
		fmt.Fprintln(w, "mtix sync harden: nothing to change.")
	} else {
		fmt.Fprintf(w, "mtix sync harden: applied %d statement(s):\n", len(r.Executed))
		for _, s := range r.Executed {
			fmt.Fprintln(w, "  "+safeText(s))
		}
	}
	after := r.After
	printHardenScope(w, after)
	if after.Clean() {
		fmt.Fprintln(w, verificationPassed)
	} else {
		fmt.Fprintln(w, "verification failed: access remains.")
		printHardenFindings(w, "Access that remains:", after, false)
	}
	printHardenInfo(w, after)
	if hint != "" {
		fmt.Fprintln(w, "\nTo keep the same roles in later runs, run: "+hint)
	}
}

// printHardenRoles lists, before any detail, the roles --apply would
// affect, so a legitimate role is never revoked by surprise.
func printHardenRoles(w io.Writer, rep *transport.PrivilegeReport) {
	var lose, grantOnly, regrant, cannot []string
	for _, f := range rep.Findings {
		switch {
		case f.Role == "":
		case f.Fix == "":
			cannot = append(cannot, f.Role)
		case f.Via == transport.FindingViaGrantOption:
			grantOnly = append(grantOnly, f.Role)
		case f.Via == transport.FindingViaGrantor:
			regrant = append(regrant, f.Role)
		default:
			lose = append(lose, f.Role)
		}
	}
	for _, row := range []struct {
		label string
		roles []string
	}{
		{"Roles that lose their access with --apply", lose},
		{"Kept roles that keep their access but can no longer grant it", grantOnly},
		{"Kept roles whose privileges the owner grants again", regrant},
		{"Roles whose access --apply cannot remove", cannot},
	} {
		if names := distinctSorted(row.roles); len(names) > 0 {
			fmt.Fprintf(w, "%s: %s.\n", row.label, safeText(strings.Join(names, ", ")))
		}
	}
}

// distinctSorted returns the distinct values of in, sorted.
func distinctSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// printHardenScope writes the schema, owners and kept roles.
func printHardenScope(w io.Writer, rep *transport.PrivilegeReport) {
	kept := "none"
	if len(rep.KeptRoles) > 0 {
		kept = strings.Join(rep.KeptRoles, ", ")
	}
	fmt.Fprintf(w, "Sync tables in schema %s, owned by %s. Kept roles: %s.\n",
		safeText(rep.Schema), safeText(strings.Join(rep.Owners, ", ")), kept)
}

// printHardenFindings writes the findings that have a fix (fixable) or
// that do not, under title; nothing when there are none.
func printHardenFindings(w io.Writer, title string, rep *transport.PrivilegeReport, fixable bool) {
	var lines []string
	for _, f := range rep.Findings {
		if (f.Fix != "") != fixable {
			continue
		}
		lines = append(lines, "  "+findingLine(f))
		if f.Manual != "" {
			lines = append(lines, "      an administrator runs: "+safeText(f.Manual))
		}
		if f.Note != "" {
			lines = append(lines, "      "+safeText(f.Note))
		}
	}
	if len(lines) == 0 {
		return
	}
	fmt.Fprintln(w, "\n"+title)
	fmt.Fprintln(w, strings.Join(lines, "\n"))
}

// printHardenInfo writes the information items, which do not fail
// verification: EXECUTE on the mtix trigger functions and other roles'
// default privileges. Each carries a note that says why.
func printHardenInfo(w io.Writer, rep *transport.PrivilegeReport) {
	if len(rep.Info) == 0 {
		return
	}
	fmt.Fprintln(w, "\nInformation (does not fail verification):")
	for _, f := range rep.Info {
		fmt.Fprintln(w, "  "+findingLine(f))
		if f.Note != "" {
			fmt.Fprintln(w, "      "+safeText(f.Note))
		}
	}
}

// findingLine renders one finding on one line.
func findingLine(f transport.PrivilegeFinding) string {
	role := f.Role
	if role == "" {
		role = "-"
	}
	what := f.Kind + " " + safeText(f.Object)
	if f.Kind == transport.FindingKindDefaultACL {
		what = safeText(f.Object) // already reads "default privileges of ..."
	}
	line := fmt.Sprintf("%-28s %s", safeText(role), what)
	if len(f.Privileges) > 0 {
		line += " (" + strings.Join(f.Privileges, ", ") + ")"
	}
	via := f.Via
	if f.Scope != "" {
		via += ", " + f.Scope
	}
	return line + " [" + via + "]"
}

// keepFlags renders the kept roles as --keep-role flags.
func keepFlags(kept []string) string {
	var b strings.Builder
	for _, k := range kept {
		b.WriteString(" --keep-role " + k)
	}
	return b.String()
}

// safeText returns s unchanged when it is printable ASCII, else quoted, so
// a name read from the catalog cannot write control characters to a
// terminal.
func safeText(s string) string {
	for _, r := range s {
		if r < 0x20 || r > 0x7e {
			return strconv.QuoteToASCII(s)
		}
	}
	return s
}
