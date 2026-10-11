// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Inbox lease commands derive local identity and expose typed ownership outcomes.
package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

var errLeaseHeld = errors.New("inbox lease held")

type leaseHeldError struct {
	agent   string
	expires time.Time
	stale   bool
	cause   error
}

func (e *leaseHeldError) Error() string {
	if e.stale {
		return "not the holder of this lease; it expired and was taken over or released"
	}
	return fmt.Sprintf("lease for %s held by another run until %s; marked dirty, the holder re-checks before release", e.agent, e.expires.Format(time.RFC3339Nano))
}

func (e *leaseHeldError) Unwrap() []error { return []error{errLeaseHeld, e.cause} }

type leaseOptions struct {
	agent, holder, ttl string
	abandon, json      bool
}

func newInboxLeaseCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "lease", Short: "Acquire or release board-local wake ownership", Args: cobra.NoArgs}
	cmd.AddCommand(newLeaseOperationCmd("acquire"), newLeaseOperationCmd("release"))
	return cmd
}

func newLeaseOperationCmd(verb string) *cobra.Command {
	var opts leaseOptions
	var registrationErr error
	cmd := &cobra.Command{Use: verb, Short: verb + " board-local wake ownership", Args: cobra.NoArgs,
		Long: "Coordinate wake runs for this agent on this host and user. TTL defaults to 15m (1m..24h).\nExit 6 means held or no longer the holder; storage failures retain exit 3/4.\nAcquire prints the owner token; use --holder to renew. Release prints released, relaunch or abandoned.\nExample: HOLDER=$(mtix inbox lease acquire --agent developer)\nmtix inbox lease release --agent developer --holder \"$HOLDER\"",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if registrationErr != nil {
				return fmt.Errorf("register lease flags: %w", registrationErr)
			}
			return runLeaseOperation(cmd, verb, opts)
		},
	}
	cmd.Flags().StringVar(&opts.agent, "agent", "", "Agent id (required)")
	cmd.Flags().StringVar(&opts.holder, "holder", "", "Owner token (required for release; renews acquire)")
	cmd.Flags().StringVar(&opts.ttl, "ttl", "15m", "Lease duration, inclusive 1m..24h")
	cmd.Flags().BoolVar(&opts.json, "json", false, "Print JSON")
	registrationErr = cmd.MarkFlagRequired("agent")
	if verb == "release" {
		cmd.Flags().BoolVar(&opts.abandon, "abandon", false, "Release ownership without relaunch")
		registrationErr = errors.Join(registrationErr, cmd.MarkFlagRequired("holder"))
	}
	return cmd
}

func parseLeaseTTL(value string) (time.Duration, error) {
	ttl, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid lease ttl %q (%v): %w", value, err, model.ErrInvalidInput)
	}
	if ttl < time.Minute || ttl > 24*time.Hour {
		return 0, fmt.Errorf("lease ttl must be between 1m and 24h: %w", model.ErrInvalidInput)
	}
	return ttl, nil
}

func leaseIdentity(agent string) (sqlite.WakeLeaseKey, error) {
	if agent == "" {
		return sqlite.WakeLeaseKey{}, fmt.Errorf("lease agent required: %w", model.ErrInvalidInput)
	}
	env, err := oplocal.FromProcess()
	if err != nil {
		return sqlite.WakeLeaseKey{}, fmt.Errorf("lease identity: %w", err)
	}
	host, err := oplocal.New(env).HostID()
	if err != nil {
		return sqlite.WakeLeaseKey{}, fmt.Errorf("lease host identity: %w", err)
	}
	uid, err := currentUserID()
	if err != nil {
		return sqlite.WakeLeaseKey{}, fmt.Errorf("lease user identity: %w", err)
	}
	return sqlite.WakeLeaseKey{AgentID: agent, HostID: host, UID: uid}, nil
}

func runLeaseOperation(cmd *cobra.Command, verb string, opts leaseOptions) error {
	ttl, err := parseLeaseTTL(opts.ttl)
	if err != nil {
		return err
	}
	if app.store == nil {
		return fmt.Errorf("not in an mtix project (run 'mtix init' first)")
	}
	key, err := leaseIdentity(opts.agent)
	if err != nil {
		return err
	}
	if verb == "acquire" {
		lease, acquireErr := app.store.AcquireWakeLease(cmd.Context(), key, opts.holder, ttl)
		if acquireErr != nil {
			if errors.Is(acquireErr, model.ErrAlreadyClaimed) {
				return &leaseHeldError{agent: opts.agent, expires: lease.ExpiresAt, cause: acquireErr}
			}
			return acquireErr
		}
		return writeLeaseOutput(cmd, opts.json || app.jsonOutput, lease, lease.Holder)
	}
	result, err := app.store.ReleaseWakeLease(cmd.Context(), key, opts.holder, ttl, opts.abandon)
	if err != nil {
		if errors.Is(err, model.ErrAlreadyClaimed) {
			return &leaseHeldError{stale: true, cause: err}
		}
		return err
	}
	return writeLeaseOutput(cmd, opts.json || app.jsonOutput, result, result.Outcome)
}
