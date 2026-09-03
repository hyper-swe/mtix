// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MTIX-90: the unbypassable confirmation for commands that bulk-delete
// tickets.
//
// A command that destroys a team's ticket history gets mtix thrown out of
// a deployment, so such a command must (1) say exactly what it is about
// to destroy and how many rows, (2) require a confirmation that no flag
// can supply, and (3) refuse outright when stdin is not a terminal so
// automation fails closed instead of silently wiping a store. --yes and
// --force select execution over preview; they never stand in for the
// typed answer.
//
// The typed value is the ticket count printed in the warning. Unlike a
// fixed word ("yes") it cannot be baked into a script or alias, and unlike
// the project prefix it is unambiguous for a store holding several
// projects. Typing it proves the operator read the line that says how
// much is about to go.
//
// Every gated path also takes a verified snapshot of the database first
// (VACUUM INTO, the same copy 'mtix backup' makes), so a confirmed mistake
// is recoverable rather than terminal. A snapshot that cannot be written
// aborts the command: the usual reason is a full disk, which is exactly
// when a delete must not run.
//
// docs/DESTRUCTIVE-COMMANDS.md carries the inventory of every command this
// applies to and the recorded position on each one that is not gated.

// destructiveInput is where the confirmation is read from. Defaults to
// stdin; tests swap it to drive the prompt, mirroring createInput.
var destructiveInput io.Reader = os.Stdin

// destructiveStdinIsTerminal reports whether stdin is an interactive
// terminal. Tests override it to simulate a terminal; the piped-stdin
// test leaves the real check in place.
var destructiveStdinIsTerminal = stdinIsTerminal

func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// errDestructiveRefused is the sentinel for every refusal the guard
// issues: not a terminal, wrong token, snapshot failure. Callers wrap it
// so the command name is on the message; tests match on it.
var errDestructiveRefused = errors.New("destructive command refused")

// destroyCount is one line of the "destroys" summary: a label and the
// exact number of rows it covers.
type destroyCount struct {
	Label string
	N     int
}

// destructiveOp describes one irreversible operation for the guard.
type destructiveOp struct {
	// Command is the exact invocation, e.g. "mtix sync reconcile --discard-local".
	Command string
	// Scope names what is affected: the local store path and its projects,
	// or a hub.
	Scope string
	// Destroys lists what is deleted. The FIRST entry is the ticket count
	// and its N is the value the operator must type.
	Destroys []destroyCount
	// Consequence states plainly what the world looks like afterwards.
	Consequence string
	// SnapshotTag names the pre-destroy snapshot file, e.g. "discard-local".
	SnapshotTag string
}

// tickets returns the count the operator must type.
func (op destructiveOp) tickets() int {
	if len(op.Destroys) == 0 {
		return 0
	}
	return op.Destroys[0].N
}

// token is the exact string the operator must type.
func (op destructiveOp) token() string { return strconv.Itoa(op.tickets()) }

// nothingToDestroy reports whether every count is zero, in which case
// there is nothing for the guard to protect.
func (op destructiveOp) nothingToDestroy() bool {
	for _, d := range op.Destroys {
		if d.N != 0 {
			return false
		}
	}
	return true
}

// destroysLine renders "27 tickets, 140 journal events".
func (op destructiveOp) destroysLine() string {
	parts := make([]string, 0, len(op.Destroys))
	for _, d := range op.Destroys {
		parts = append(parts, fmt.Sprintf("%d %s", d.N, d.Label))
	}
	return strings.Join(parts, ", ")
}

// snapshotPath is where the pre-destroy snapshot goes: beside the rolling
// backups but under a different prefix, so the scheduler's rotation of
// mtix-*.db never removes it.
func (op destructiveOp) snapshotPath(mtixDir string, now time.Time) string {
	return filepath.Join(mtixDir, "data", "backups",
		fmt.Sprintf("pre-%s-%s.db", op.SnapshotTag, now.UTC().Format("20060102-150405")))
}

// confirmDestructive prints the warning for op to w, then requires the
// ticket count typed on stdin. It returns nil only when the operator
// typed the exact count at an interactive terminal, or when there is
// nothing to destroy (which it says). Every other outcome is an error
// wrapping errDestructiveRefused, and nothing has been mutated.
func confirmDestructive(w io.Writer, op destructiveOp) error {
	if op.nothingToDestroy() {
		fmt.Fprintf(w, "%s: nothing to destroy (%s); no confirmation required\n",
			op.Command, op.destroysLine())
		return nil
	}

	fmt.Fprintf(w, "WARNING: %s is irreversible.\n", op.Command)
	fmt.Fprintf(w, "  scope:    %s\n", op.Scope)
	fmt.Fprintf(w, "  destroys: %s\n", op.destroysLine())
	fmt.Fprintf(w, "  after:    %s\n", op.Consequence)
	fmt.Fprintln(w, "No flag can confirm this: --yes and --force do not bypass this prompt.")

	if !destructiveStdinIsTerminal() {
		return fmt.Errorf("%s: %w: stdin is not a terminal. This command destroys %s and "+
			"requires the ticket count typed at an interactive terminal; no flag can supply it. "+
			"Nothing was changed",
			op.Command, errDestructiveRefused, op.destroysLine())
	}

	fmt.Fprintf(w, "Type the number of tickets to be destroyed (%s) to continue, anything else to abort: ", op.token())
	line, _ := bufio.NewReader(destructiveInput).ReadString('\n')
	answer := strings.TrimSpace(line)
	if answer != op.token() {
		return fmt.Errorf("%s: %w: expected %q, got %q; nothing was changed",
			op.Command, errDestructiveRefused, op.token(), answer)
	}
	return nil
}

// snapshotBeforeDestroy writes a verified copy of the store to the
// snapshot path and reports where. A failure aborts the caller: with no
// snapshot there is no recovery, and a store that cannot be copied is
// usually one on a full disk.
func snapshotBeforeDestroy(ctx context.Context, w io.Writer, op destructiveOp) (string, error) {
	if app.store == nil || app.mtixDir == "" {
		return "", fmt.Errorf("%s: %w: no local store to snapshot", op.Command, errDestructiveRefused)
	}
	dest := op.snapshotPath(app.mtixDir, time.Now())
	if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
		return "", fmt.Errorf("%s: %w: create snapshot dir: %v", op.Command, errDestructiveRefused, err)
	}
	result, err := app.store.Backup(ctx, dest)
	if err != nil {
		return "", fmt.Errorf("%s: %w: pre-destroy snapshot failed, nothing was changed: %v",
			op.Command, errDestructiveRefused, err)
	}
	fmt.Fprintf(w, "snapshot: %s (%d bytes, verified: %t)\n", result.Path, result.Size, result.Verified)
	return result.Path, nil
}

// guardDestructive is the full sequence every gated command runs before
// its first DELETE: warn, require the typed count, snapshot. It returns
// the snapshot path ("" when there was nothing to destroy).
func guardDestructive(ctx context.Context, w io.Writer, op destructiveOp) (string, error) {
	if err := confirmDestructive(w, op); err != nil {
		return "", err
	}
	if op.nothingToDestroy() {
		return "", nil
	}
	return snapshotBeforeDestroy(ctx, w, op)
}

// countRows returns count(*) for one table of the local store.
func countRows(ctx context.Context, table string) (int, error) {
	var n int
	// table is a compile-time constant at every call site.
	if err := app.store.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

// localScope renders the store path and the projects it holds.
func localScope(ctx context.Context) string {
	scope := "local store " + app.mtixDir
	projects, err := app.store.DistinctProjects(ctx)
	if err != nil || len(projects) == 0 {
		return scope
	}
	names := make([]string, 0, len(projects))
	for _, p := range projects {
		names = append(names, p.Prefix)
	}
	return scope + " (projects: " + strings.Join(names, ", ") + ")"
}
