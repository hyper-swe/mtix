// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// AlertRequest supplies bounded operator diagnostic text (MTIX-112.17.11).
type AlertRequest struct{ Agent, Class, Detail, Fix string }

// Alerts records operator-local diagnostics without board or wake capabilities.
type Alerts struct {
	state *oplocal.State
	clock func() time.Time
}

// NewAlerts constructs an operator diagnostic service with an injected clock.
func NewAlerts(state *oplocal.State, clock func() time.Time) *Alerts {
	if state == nil || clock == nil {
		panic("alerts: state and clock must not be nil")
	}
	return &Alerts{state: state, clock: clock}
}

// Raise records one occurrence; a returned error may follow durable acceptance.
func (a *Alerts) Raise(ctx context.Context, input AlertRequest) (oplocal.Alert, error) {
	if input.Agent == "" || !utf8.ValidString(input.Agent) {
		return oplocal.Alert{}, fmt.Errorf("alert agent is invalid: %w", model.ErrInvalidInput)
	}
	if err := validateAgentID(input.Agent); err != nil {
		return oplocal.Alert{}, fmt.Errorf("validate alert agent: %w", err)
	}
	row, err := a.state.RecordAlert(ctx, oplocal.Alert{Agent: input.Agent, Class: input.Class, Detail: input.Detail, Fix: input.Fix}, a.clock())
	if err != nil {
		return row, fmt.Errorf("raise operator alert: %w", err)
	}
	return row, nil
}

// Recent reads newest-first logical diagnostics; zero selects all retained rows.
func (a *Alerts) Recent(ctx context.Context, limit int) ([]oplocal.Alert, error) {
	rows, err := a.state.RecentAlerts(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("read operator alerts: %w", err)
	}
	return rows, nil
}
