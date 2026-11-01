// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// errClaudeHookWake marks a hook run that must exit 2 so the Claude harness
// wakes the session with the envelope already written to stderr.
var errClaudeHookWake = errors.New("claude hook: wake")

const (
	claudeHookLeaseTTL   = 15 * time.Minute // one waiter per registration; re-armed by the next hook
	claudeHookClaimLease = 10 * time.Minute
)

// newHookCmd creates the `mtix hook` group: programs that a harness runs as
// lifecycle hooks (MTIX-112.40.4). Installing them comes with MTIX-112.40.5.
func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "hook", Short: "Harness hook programs (run by a Claude Code hook, not by hand)"}
	cmd.AddCommand(newClaudeSessionHookCmd())
	return cmd
}

func newClaudeSessionHookCmd() *cobra.Command {
	var agent string
	var timeout int
	cmd := &cobra.Command{
		Use:   "claude-session <session-start|stop>",
		Short: "Claude Code session hook: register, arm one inbox waiter, wake the session with exit 2",
		Long: "Run by a Claude Code hook with asyncRewake. session-start registers --agent and arms its waiter; stop arms it again.\n" +
			"One waiter runs per registration. When addressed mail arrives the wake policy and the wake ledger are applied once per event,\n" +
			"the quoted envelope goes to stderr and the exit code is 2, which wakes an idle session. Any internal error exits 0 and is recorded:\n" +
			"mtix doctor shows it as wake.listener.hook-errors with the command that re-runs the hook.",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{string(service.ClaudeHookSessionStart), string(service.ClaudeHookStop)},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runClaudeSessionHook(cmd.ErrOrStderr(), service.ClaudeHookEvent(args[0]), agent, timeout)
		},
	}
	cmd.Flags().StringVar(&agent, "agent", "", "Agent id (required)")
	cmd.Flags().IntVar(&timeout, "timeout", 3600, "Seconds the waiter stays armed before it exits 0 (0 = until woken)")
	_ = cmd.MarkFlagRequired("agent")
	return cmd
}

// claudeHookLog is the hook error record directory of this board.
func claudeHookLog() *service.ClaudeHookLog {
	return &service.ClaudeHookLog{Dir: filepath.Join(app.mtixDir, "data", "claude-hooks"), Clock: time.Now}
}

// runClaudeSessionHook never fails a session: only the wake sentinel is returned.
func runClaudeSessionHook(stderr io.Writer, ev service.ClaudeHookEvent, agent string, timeout int) error {
	if app.store == nil || timeout < 0 {
		fmt.Fprintf(stderr, "mtix hook claude-session: not armed (no mtix project here, or a negative --timeout)\n")
		return nil
	}
	hook, err := newClaudeSessionHook(agent, time.Duration(timeout)*time.Second)
	if err != nil {
		if rerr := claudeHookLog().Record(agent, ev, err); rerr != nil {
			err = errors.Join(err, rerr)
		}
		fmt.Fprintf(stderr, "mtix hook claude-session: %v\n", err)
		return nil
	}
	res := hook.Run(sessionContext(context.Background()), ev)
	if res.ExitCode == service.ClaudeHookExitWake {
		fmt.Fprint(stderr, res.Envelope)
		return errClaudeHookWake
	}
	fmt.Fprintf(stderr, "mtix hook claude-session: %s %s\n", res.Outcome, res.Detail)
	return nil
}

func newClaudeSessionHook(agent string, wait time.Duration) (*service.ClaudeHook, error) {
	key, err := leaseIdentity(agent)
	if err != nil {
		return nil, err
	}
	perms, board, err := configuredPermissions()
	if err != nil {
		return nil, err
	}
	return service.NewClaudeHook(service.ClaudeHookDeps{
		Key: key,
		Register: func(ctx context.Context) error {
			_, rerr := registerCurrentSession(ctx, agent, sessionOptions{})
			return rerr
		},
		Epoch: func(ctx context.Context) (int64, error) {
			return registrationEpoch(ctx, key)
		},
		Wakeable: func(ctx context.Context) (bool, string, error) { return perms.Wakeable(ctx, board, agent) },
		Wait: func(ctx context.Context, skip func(int64) bool) ([]sqlite.InboxEvent, error) {
			return claudeHookWait(ctx, agent, wait, skip, joinListener)
		},
		Render: func(events []sqlite.InboxEvent) (string, error) {
			caps, cerr := loadInboxEnvelopeCaps(agent)
			if cerr != nil {
				return "", cerr
			}
			return formatInboxPrompt(agent, events, caps)
		},
		Leases:     service.NewWakeLeaseService(app.store),
		Claims:     app.store,
		Log:        claudeHookLog(),
		LeaseTTL:   claudeHookLeaseTTL,
		ClaimLease: claudeHookClaimLease,
	}), nil
}

// registrationEpoch is the epoch of this host and user's registration of the agent.
func registrationEpoch(ctx context.Context, key service.WakeLeaseKey) (int64, error) {
	rows, err := app.store.ListRegistrations(ctx, key.HostID, key.UID)
	if err != nil {
		return 0, fmt.Errorf("list registrations: %w", err)
	}
	for _, r := range rows {
		if r.AgentID == key.AgentID {
			return r.Epoch, nil
		}
	}
	return 0, fmt.Errorf("agent %s is not registered on this host: run the session-start hook first", key.AgentID)
}

// claudeHookJoin joins the board listener for the duration of a wait and
// returns the function that ends the join.
type claudeHookJoin func(agent string, out io.Writer) func()

// claudeHookPollInterval matches the poll of `mtix inbox --wait`.
const claudeHookPollInterval = 250 * time.Millisecond

// claudeHookWait is the level-triggered wait of `mtix inbox --wait`, including
// the board listener lease (MTIX-112.20.4), except that events for which skip
// is true do not end it: a re-arm waits for new mail only. A signal or the
// timeout (0 = none) ends it quietly with no events.
func claudeHookWait(ctx context.Context, agent string, timeout time.Duration, skip func(int64) bool, join claudeHookJoin) ([]sqlite.InboxEvent, error) {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	defer join(agent, io.Discard)()
	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	ticker := time.NewTicker(claudeHookPollInterval)
	defer ticker.Stop()
	for {
		events, err := app.store.InboxList(ctx, agent)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil
			}
			return nil, fmt.Errorf("list inbox: %w", err)
		}
		var fresh []sqlite.InboxEvent
		for _, e := range events {
			if !skip(e.Seq) {
				fresh = append(fresh, e)
			}
		}
		if len(fresh) > 0 {
			return fresh, nil
		}
		select {
		case <-waitCtx.Done():
			return nil, nil
		case <-ticker.C:
		}
	}
}
