// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"
)

// defaultExecTimeout bounds a hook command that omits timeout-seconds. Under
// the detached-spawn model this is a best-effort kill from the spawning
// process (a long-lived daemon enforces it; an ephemeral CLI that exits first
// cannot) — scripts should self-bound.
const defaultExecTimeout = 30 * time.Second

// ExecAdapter runs a hook's configured argv when it fires (FR-19.3). The event
// JSON reaches the process ONLY via the MTIX_EVENT environment variable and on
// stdin — never interpolated into a command line, and never through a shell
// (the argv is exec'd directly).
//
// Delivery is a DETACHED SPAWN (MTIX-56.9): Deliver returns as soon as the
// process has started, so dispatch never blocks the mutation path (the FR-19
// async contract) — a successful spawn is the terminal 'delivered' outcome.
// The command's own exit code is the script's responsibility to report (it is
// logged here best-effort when the spawning process lives long enough to reap
// it); the fabric's true success signal is the inbox getting acked. The child
// runs in its own session, so it survives an ephemeral CLI parent exiting.
//
// This adapter is the sharp security edge, so the dispatcher gates it behind
// the content-hash trust (see trust.go / ExecTrusted) and the local
// exec-dispatch policy (policy.go): it fires ONLY for a hooks.yaml the local
// operator has explicitly trusted, so a synced or edited config can never run
// code unbidden. The adapter itself assumes those gates have already passed.
type ExecAdapter struct{}

// NewExecAdapter constructs the exec adapter.
func NewExecAdapter() *ExecAdapter { return &ExecAdapter{} }

// Name returns the adapter's deliver: key.
func (a *ExecAdapter) Name() string { return AdapterExec }

// Deliver spawns the hook's command detached with the event on env + stdin.
// An error means the SPAWN failed (missing script, permission) — the command
// having started is success, whatever it later exits with.
func (a *ExecAdapter) Deliver(_ context.Context, d Delivery) error {
	cfg := d.Hook.Exec
	if cfg == nil || len(cfg.Command) == 0 {
		return fmt.Errorf("exec adapter: hook %q has no command", d.Hook.Name)
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}

	// #nosec G204 -- argv is operator-authored config, trusted by content hash
	// (47.5) and run WITHOUT a shell. Event data reaches the process only via
	// env/stdin, never the command line, so it cannot inject arguments.
	// Deliberately NOT CommandContext: the child must survive dispatch/daemon
	// context cancellation and an ephemeral parent's exit (own session below);
	// the best-effort timeout below is the only kill switch.
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...) //nolint:noctx // detached by design (MTIX-56.9): ctx cancellation must not kill an in-flight wake
	// MTIX_HOOK_ORIGIN lets any mtix mutation the command runs stamp its events
	// with this hook's name, so they never re-trigger it (loop prevention, 47.7).
	cmd.Env = append(cmd.Environ(),
		"MTIX_EVENT="+string(d.EventJSON),
		"MTIX_HOOK_ORIGIN="+d.Hook.Name)
	cmd.Stdin = bytes.NewReader(d.EventJSON)
	cmd.SysProcAttr = detachedSysProcAttr() // own session: parent exit never kills the wake

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("exec adapter: hook %q spawn %v failed: %w", d.Hook.Name, cfg.Command, err)
	}

	// Best-effort supervision while THIS process lives: enforce the timeout
	// and reap the child (no zombies in a long-lived daemon), logging a
	// non-zero exit for operator visibility. None of it changes the ledger —
	// the outcome was decided at spawn (see type comment).
	timer := time.AfterFunc(timeout, func() {
		_ = cmd.Process.Kill()
	})
	go func() {
		err := cmd.Wait()
		timer.Stop()
		if err != nil {
			slog.Warn("hook exec exited with error (ledger outcome unchanged — scripts self-report, inbox ack is the success signal)",
				"hook", d.Hook.Name, "error", err)
		}
	}()
	return nil
}
