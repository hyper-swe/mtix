// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"

	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// LiveState is the outcome of probing a registration's session.
type LiveState int

const (
	// LiveUnknown means the probe could not decide. Callers fail safe: an
	// unknown row is treated as live and is never replaced silently.
	LiveUnknown LiveState = iota
	// LiveAlive means the registered session is still running.
	LiveAlive
	// LiveDead means the registered session is gone (exited or pid reused).
	LiveDead
)

// String returns the lower-case name used in listings and JSON.
func (l LiveState) String() string {
	switch l {
	case LiveAlive:
		return "alive"
	case LiveDead:
		return "dead"
	default:
		return "unknown"
	}
}

// Liveness probes whether a registration's session is still running. Harness
// adapters implement it later (ADR-007 §2.2); the default is pid plus start time.
type Liveness interface {
	Probe(ctx context.Context, reg sqlite.Registration) LiveState
}

// ProcessLiveness is the default Liveness: a registered pid is alive only when
// its recorded start time still matches, so a reused pid reads as dead.
type ProcessLiveness struct {
	startTime func(pid int) (string, error)
}

// NewProcessLiveness builds the default probe around a start-time reader; pass
// procinfo.StartTime in production.
func NewProcessLiveness(startTime func(pid int) (string, error)) *ProcessLiveness {
	return &ProcessLiveness{startTime: startTime}
}

// Probe implements Liveness (ADR-007 §3.4, TM I7). A row with no pid or no
// recorded start time, or a platform that cannot read start times, is unknown.
func (p *ProcessLiveness) Probe(_ context.Context, reg sqlite.Registration) LiveState {
	if reg.PID <= 0 || reg.PIDStartTime == "" {
		return LiveUnknown
	}
	start, err := p.startTime(int(reg.PID))
	switch {
	case errors.Is(err, procinfo.ErrNoProcess):
		return LiveDead
	case err != nil:
		return LiveUnknown
	case start == reg.PIDStartTime:
		return LiveAlive
	default:
		return LiveDead
	}
}
