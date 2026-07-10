// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// defaultExecTimeout bounds a hook command that omits timeout-seconds.
const defaultExecTimeout = 30 * time.Second

// ExecAdapter runs a hook's configured argv when it fires (FR-19.3). The event
// JSON reaches the process ONLY via the MTIX_EVENT environment variable and on
// stdin — never interpolated into a command line, and never through a shell
// (the argv is exec'd directly). A timeout bounds each run.
//
// This adapter is the sharp security edge, so the dispatcher gates it behind the
// content-hash trust (see trust.go / ExecTrusted): it fires ONLY for a
// hooks.yaml the local operator has explicitly trusted, so a synced or edited
// config can never run code unbidden. The adapter itself assumes that gate has
// already passed.
type ExecAdapter struct{}

// NewExecAdapter constructs the exec adapter.
func NewExecAdapter() *ExecAdapter { return &ExecAdapter{} }

// Name returns the adapter's deliver: key.
func (a *ExecAdapter) Name() string { return AdapterExec }

// Deliver runs the hook's command with the event on env + stdin.
func (a *ExecAdapter) Deliver(ctx context.Context, d Delivery) error {
	cfg := d.Hook.Exec
	if cfg == nil || len(cfg.Command) == 0 {
		return fmt.Errorf("exec adapter: hook %q has no command", d.Hook.Name)
	}
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 -- argv is operator-authored config, trusted by content hash
	// (47.5) and run WITHOUT a shell. Event data reaches the process only via
	// env/stdin, never the command line, so it cannot inject arguments.
	cmd := exec.CommandContext(runCtx, cfg.Command[0], cfg.Command[1:]...)
	// MTIX_HOOK_ORIGIN lets any mtix mutation the command runs stamp its events
	// with this hook's name, so they never re-trigger it (loop prevention, 47.7).
	cmd.Env = append(cmd.Environ(),
		"MTIX_EVENT="+string(d.EventJSON),
		"MTIX_HOOK_ORIGIN="+d.Hook.Name)
	cmd.Stdin = bytes.NewReader(d.EventJSON)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exec adapter: hook %q command %v failed: %w (output: %s)",
			d.Hook.Name, cfg.Command, err, truncate(out.String(), 500))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
