// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// staleAfter is how long a row may go unseen before a failed probe expires it.
const staleAfter = 24 * time.Hour

// RegistrationView is a registration with its probed liveness for listings.
type RegistrationView struct {
	sqlite.Registration
	Liveness  string `json:"liveness"`
	Hold      bool   `json:"hold"`
	LiveRoute bool   `json:"live_route"`
}

// ListRegistrations returns this host's and uid's registrations with liveness,
// hold state and whether a live route exists (I30, I31).
func (s *RegistrationService) ListRegistrations(ctx context.Context) ([]RegistrationView, error) {
	rows, err := s.Store.ListRegistrations(ctx, s.Host.HostID, s.Host.UID)
	if err != nil {
		return nil, err
	}
	out := make([]RegistrationView, 0, len(rows))
	for i := range rows {
		state := s.Liveness.Probe(ctx, rows[i])
		out = append(out, RegistrationView{Registration: rows[i], Liveness: state.String(), Hold: rows[i].PendingHold,
			LiveRoute: !hasNoRoute(&rows[i]) && state != LiveDead})
	}
	return out, nil
}

// Unregister removes an id's registration on this host. The owning session may
// remove its own row and anyone may remove a dead one; a live row of another
// session needs force (the operator's --force).
func (s *RegistrationService) Unregister(ctx context.Context, agentID string, who SessionIdentity, force bool) error {
	rows, err := s.Store.ListRegistrations(ctx, s.Host.HostID, s.Host.UID)
	if err != nil {
		return err
	}
	for i := range rows {
		if rows[i].AgentID != agentID {
			continue
		}
		if !force && !who.matches(&rows[i]) && !hasNoRoute(&rows[i]) && s.Liveness.Probe(ctx, rows[i]) != LiveDead {
			return fmt.Errorf("agent %q belongs to another live session; use --force to remove it: %w", agentID, model.ErrAgentStillActive)
		}
		n, err := s.Store.DeleteRegistrationIf(ctx, s.key(agentID), snapshotOf(&rows[i]))
		if err != nil {
			return err
		}
		if n == 0 { // a register committed after the row was read
			return fmt.Errorf("registration for %q changed while it was being removed; retry: %w", agentID, model.ErrConflict)
		}
		return nil
	}
	return fmt.Errorf("no registration for agent %q on this host: %w", agentID, model.ErrNotFound)
}

// ExpireStale deletes rows unseen for 24 hours whose probe reports dead
// (ADR-007 §3.4). Unknown liveness is kept (fail safe). It returns the rows removed.
func (s *RegistrationService) ExpireStale(ctx context.Context) ([]sqlite.Registration, error) {
	rows, err := s.Store.ListRegistrations(ctx, s.Host.HostID, s.Host.UID)
	if err != nil {
		return nil, err
	}
	cutoff := s.Clock().UTC().Add(-staleAfter)
	var expired []sqlite.Registration
	for i := range rows {
		seen, perr := time.Parse(time.RFC3339, rows[i].LastSeen)
		if perr == nil && seen.After(cutoff) {
			continue
		}
		if s.Liveness.Probe(ctx, rows[i]) != LiveDead {
			continue
		}
		n, derr := s.Store.DeleteRegistrationIf(ctx, s.key(rows[i].AgentID), snapshotOf(&rows[i]))
		if derr != nil {
			return expired, derr
		}
		if n > 0 {
			expired = append(expired, rows[i])
		}
	}
	return expired, nil
}

func snapshotOf(r *sqlite.Registration) sqlite.RegistrationSnapshot {
	return sqlite.RegistrationSnapshot{Epoch: r.Epoch, LastSeen: r.LastSeen}
}
