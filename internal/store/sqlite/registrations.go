// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Registration is one board-local session registration row (ADR-007 §3.2,
// MTIX-112.17.4). It is routing metadata only: never synced, exported or
// applied from a hub (I8), and no wake-time decision trusts it (I7).
type Registration struct {
	AgentID          string `json:"agent_id"`
	HostID           string `json:"host_id"`
	UID              string `json:"uid"`
	Harness          string `json:"harness"`
	SessionHandle    string `json:"session_handle,omitempty"`
	PID              int64  `json:"pid,omitempty"`
	PIDStartTime     string `json:"pid_start_time,omitempty"`
	PermissionMode   string `json:"permission_mode,omitempty"`
	Origin           string `json:"origin"`
	Epoch            int64  `json:"epoch"`
	HighWater        int64  `json:"high_water"`
	PendingHold      bool   `json:"pending_hold"`
	RegisteredAt     string `json:"registered_at"`
	LastSeen         string `json:"last_seen"`
	RegistrarVersion string `json:"registrar_version"`
	AttachProtocol   int64  `json:"attach_protocol"`
	// StaleSince is when the wake scanner first saw the session dead; empty when not marked.
	StaleSince string `json:"stale_since,omitempty"`
}

// RegistrationKey identifies a registration: one row per agent, host and
// operating-system user (I31: other uids are never read).
type RegistrationKey struct {
	AgentID string
	HostID  string
	UID     string
}

const registrationColumns = `agent_id, host_id, uid, harness, COALESCE(session_handle,''),
	COALESCE(pid,0), COALESCE(pid_start_time,''), COALESCE(permission_mode,''), origin, epoch,
	high_water, pending_hold, registered_at, last_seen, registrar_version, attach_protocol,
	COALESCE(stale_since,'')`

type registrationScanner interface{ Scan(dest ...any) error }

func scanRegistration(r registrationScanner) (*Registration, error) {
	var (
		reg  Registration
		hold int64
	)
	if err := r.Scan(&reg.AgentID, &reg.HostID, &reg.UID, &reg.Harness, &reg.SessionHandle,
		&reg.PID, &reg.PIDStartTime, &reg.PermissionMode, &reg.Origin, &reg.Epoch,
		&reg.HighWater, &hold, &reg.RegisteredAt, &reg.LastSeen, &reg.RegistrarVersion, &reg.AttachProtocol, &reg.StaleSince); err != nil {
		return nil, err
	}
	reg.PendingHold = hold != 0
	return &reg, nil
}

// RegisterSessionAtomic runs decide inside one write transaction with the
// current row for key (nil when absent) and the journal maximum (the initial
// high-water, A3: registering never wakes on backlog). A non-nil result
// replaces the row in the same transaction; nil leaves it untouched. The
// stored row is returned. FR-19 / ADR-007 §3.4.
func (s *Store) RegisterSessionAtomic(
	ctx context.Context, key RegistrationKey,
	decide func(existing *Registration, journalMax int64) (*Registration, error),
) (*Registration, error) {
	var out *Registration
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// Read the single row for this agent on this host and uid only (I31).
		existing, err := scanRegistration(tx.QueryRowContext(ctx,
			`SELECT `+registrationColumns+` FROM agent_registrations WHERE agent_id = ? AND host_id = ? AND uid = ?`,
			key.AgentID, key.HostID, key.UID))
		if errors.Is(err, sql.ErrNoRows) {
			existing, err = nil, nil
		}
		if err != nil {
			return fmt.Errorf("read registration %q: %w", key.AgentID, err)
		}
		tail, err := journalTail(ctx, tx)
		if err != nil {
			return err
		}
		next, err := decide(existing, tail)
		if err != nil {
			return err
		}
		if next == nil {
			out = existing
			return nil
		}
		out = next
		return upsertRegistration(ctx, tx, next)
	})
	if err != nil {
		return nil, fmt.Errorf("register session %q: %w", key.AgentID, err)
	}
	return out, nil
}

func upsertRegistration(ctx context.Context, tx *sql.Tx, r *Registration) error {
	hold := 0
	if r.PendingHold {
		hold = 1
	}
	// Replace the row for (agent, host, uid): a dead row is replaced and a
	// take-over bumps the epoch, both decided by the caller in this same tx.
	_, err := tx.ExecContext(ctx, `
		INSERT OR REPLACE INTO agent_registrations
		(agent_id, harness, session_handle, uid, host_id, pid, pid_start_time, permission_mode,
		 origin, epoch, high_water, pending_hold, registered_at, last_seen, registrar_version, attach_protocol, capabilities)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, NULLIF(?, 0), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.AgentID, r.Harness, r.SessionHandle, r.UID, r.HostID, r.PID, r.PIDStartTime, r.PermissionMode,
		r.Origin, r.Epoch, r.HighWater, hold, r.RegisteredAt, r.LastSeen, r.RegistrarVersion, r.AttachProtocol, HistoryReaderCapability)
	if err != nil {
		return fmt.Errorf("write registration %q: %w", r.AgentID, err)
	}
	return nil
}

// ListRegistrations returns the registrations of one host and uid, ordered by
// agent id. Rows of any other uid or host are never returned (I31).
func (s *Store) ListRegistrations(ctx context.Context, hostID, uid string) ([]Registration, error) {
	// Only this host's and uid's rows: another uid's rows are invisible (I31).
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+registrationColumns+` FROM agent_registrations WHERE host_id = ? AND uid = ? ORDER BY agent_id`,
		hostID, uid)
	if err != nil {
		return nil, fmt.Errorf("list registrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Registration
	for rows.Next() {
		reg, scanErr := scanRegistration(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan registration: %w", scanErr)
		}
		out = append(out, *reg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate registrations: %w", err)
	}
	return out, nil
}

// RegistrationSnapshot is the version of a row a caller examined: a delete
// applies only while the row still has this epoch and last_seen.
type RegistrationSnapshot struct {
	Epoch    int64
	LastSeen string
}

// DeleteRegistrationIf removes the row for key only if it still has the
// snapshot's epoch and last_seen, inside one guarded write transaction, and
// reports how many rows went. A register that committed after the caller read
// the row changes both fields, so the fresh row survives (zero rows).
func (s *Store) DeleteRegistrationIf(ctx context.Context, key RegistrationKey, seen RegistrationSnapshot) (int64, error) {
	var n int64
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// Compare-and-delete: epoch and last_seen identify the exact row version examined.
		res, err := tx.ExecContext(ctx,
			`DELETE FROM agent_registrations
			  WHERE agent_id = ? AND host_id = ? AND uid = ? AND epoch = ? AND last_seen = ?`,
			key.AgentID, key.HostID, key.UID, seen.Epoch, seen.LastSeen)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("delete registration %q: %w", key.AgentID, err)
	}
	return n, nil
}

// TouchRegistration sets last_seen on the row for key, only when handle is
// non-empty and equals the row's session handle (ADR-007 §3.3: last_seen moves
// only for the registered session). It returns the rows updated.
func (s *Store) TouchRegistration(ctx context.Context, key RegistrationKey, handle, now string) (int64, error) {
	if handle == "" {
		return 0, nil
	}
	var n int64
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE agent_registrations SET last_seen = ?
			  WHERE agent_id = ? AND host_id = ? AND uid = ? AND session_handle = ?`,
			now, key.AgentID, key.HostID, key.UID, handle)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("touch registration %q: %w", key.AgentID, err)
	}
	return n, nil
}

// SessionTouch carries the calling session's identity in a context so the
// inbox list, wait and ack paths can refresh last_seen (ADR-007 §3.3).
type SessionTouch struct {
	HostID string
	UID    string
	Handle string
}

type sessionTouchKey struct{}

// WithSessionTouch attaches the session identity to ctx. Inbox reads and acks
// run with it refresh last_seen of the matching registration only.
func WithSessionTouch(ctx context.Context, t SessionTouch) context.Context {
	return context.WithValue(ctx, sessionTouchKey{}, t)
}

// sessionTouchTimeout bounds the last_seen refresh. The refresh runs detached
// from the caller's deadline, so it needs its own bound (MTIX-112.17.19).
const sessionTouchTimeout = 5 * time.Second

// touchFromContext refreshes last_seen when ctx carries a session identity.
// A failure is logged and never fails the inbox operation: last_seen is a
// liveness hint, not delivery state.
//
// The write runs on a context detached from ctx's deadline and cancellation
// (context.WithoutCancel keeps the session value) with its own short bound,
// so a poll that finishes just before an inbox wait deadline still refreshes
// last_seen instead of logging a spurious WARN on the expired wait context
// (MTIX-112.17.19, ADR-007 §3.3).
func (s *Store) touchFromContext(ctx context.Context, agentID string) {
	t, ok := ctx.Value(sessionTouchKey{}).(SessionTouch)
	if !ok || t.Handle == "" {
		return
	}
	if s.preflightWrite() != nil {
		return // writes are refused (unsafe filesystem, fail-stop, full disk): a read path must not write
	}
	key := RegistrationKey{AgentID: agentID, HostID: t.HostID, UID: t.UID}
	touchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sessionTouchTimeout)
	defer cancel()
	if _, err := s.TouchRegistration(touchCtx, key, t.Handle, s.clock().UTC().Format(time.RFC3339)); err != nil {
		s.logger.Warn("refresh registration last_seen", slog.String("agent_id", agentID), slog.Any("error", err))
	}
}
