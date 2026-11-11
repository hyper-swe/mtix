// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

// InboxParked reports whether agent has a live wait on this machine (MTIX-112.3).
// A waiter row counts only when its pid is running with the recorded start time,
// so a crashed or pid-reused waiter is never reported parked. A store without the
// waiters table (a released store opened read-only) reports not parked. It
// writes nothing.
func (s *Store) InboxParked(ctx context.Context, agent string) (Parked, error) {
	present, err := tableExists(ctx, s.readDB, "agent_inbox_waiters")
	if err != nil {
		return Parked{}, err
	}
	if !present {
		return Parked{}, nil
	}
	// Candidate waiters for the agent, earliest first.
	rows, err := s.readDB.QueryContext(ctx, `SELECT pid, pid_start_time, since FROM agent_inbox_waiters
		WHERE agent_id=? ORDER BY since ASC, pid ASC, token ASC`, agent)
	if err != nil {
		return Parked{}, fmt.Errorf("read waiters: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out Parked
	for rows.Next() {
		var pid int64
		var start, since string
		if err := rows.Scan(&pid, &start, &since); err != nil {
			return Parked{}, fmt.Errorf("scan waiter: %w", err)
		}
		if !s.waiterAlive(int(pid), start) {
			continue
		}
		out.Parked = true
		if len(out.PIDs) == 0 || !containsPID(out.PIDs, pid) {
			out.PIDs = append(out.PIDs, pid)
		}
		if t, perr := time.Parse(time.RFC3339Nano, since); perr == nil && out.Since == nil {
			out.Since = &t
		}
	}
	if err := rows.Err(); err != nil {
		return Parked{}, fmt.Errorf("iterate waiters: %w", err)
	}
	return out, nil
}

// waiterAlive is true only when pid runs now with exactly the recorded start
// time; an unreadable start time counts as not alive (never falsely parked).
func (s *Store) waiterAlive(pid int, recorded string) bool {
	start, err := s.processStart(pid)
	return err == nil && start == recorded
}

func (s *Store) processStart(pid int) (string, error) {
	if s.startTimeFn != nil {
		return s.startTimeFn(pid)
	}
	return procinfo.StartTime(pid)
}

// RegisterInboxWaiter records a live wait of agent and returns the function that
// removes exactly that wait's row. A wait path outside InboxWait (the policy
// wait of the service) holds it for the whole wait and calls the remover on
// every exit, so InboxParked is true during the wait (MTIX-112.3). Best effort:
// it never fails the wait.
func (s *Store) RegisterInboxWaiter(ctx context.Context, agent string) func() {
	return s.registerWaiter(ctx, agent)
}

// registerWaiter records a live wait and returns the function that removes
// exactly that wait's row. Every wait gets its own token, so overlapping waits of
// one agent in one process never share a row. Dead rows of the same agent are
// pruned in the same transaction as the insert, not by a timer. Best effort: a
// refused write or an unreadable own start time registers nothing and the wait
// proceeds (the agent then simply does not show as parked).
func (s *Store) registerWaiter(ctx context.Context, agent string) func() {
	noop := func() {}
	if s.preflightWrite() != nil {
		return noop
	}
	pid := os.Getpid()
	start, err := s.processStart(pid)
	if err != nil || start == "" {
		return noop
	}
	token := s.waitSeq.Add(1)
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryWriteBound)
	defer cancel()
	since := deliveryTime(s.clock())
	err = s.withWriteTx(wctx, func(tx *sql.Tx) error {
		if pruneErr := s.pruneDeadWaiters(wctx, tx, agent); pruneErr != nil {
			return pruneErr
		}
		// Record this wait under its own token.
		_, insErr := tx.ExecContext(wctx, `INSERT INTO agent_inbox_waiters(agent_id,pid,pid_start_time,token,since)
			VALUES(?,?,?,?,?)`, agent, pid, start, token, since)
		return insErr
	}, false)
	if err != nil {
		s.logger.Warn("record inbox waiter", slog.String("agent_id", agent), slog.Any("error", err))
		return noop
	}
	return func() { s.removeWaiter(ctx, agent, pid, start, token) }
}

func (s *Store) removeWaiter(ctx context.Context, agent string, pid int, start string, token int64) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryWriteBound)
	defer cancel()
	err := s.withWriteTx(dctx, func(tx *sql.Tx) error {
		// Remove exactly this wait on every exit path.
		_, execErr := tx.ExecContext(dctx, `DELETE FROM agent_inbox_waiters
			WHERE agent_id=? AND pid=? AND pid_start_time=? AND token=?`, agent, pid, start, token)
		return execErr
	}, false)
	if err != nil {
		s.logger.Warn("remove inbox waiter", slog.String("agent_id", agent), slog.Any("error", err))
	}
}

type waiterKey struct {
	pid   int64
	start string
	token int64
}

// pruneDeadWaiters deletes the agent's rows whose process is gone or has a
// different start time, inside the caller's transaction.
func (s *Store) pruneDeadWaiters(ctx context.Context, tx *sql.Tx, agent string) error {
	// Read the agent's rows first: liveness is probed outside SQL.
	rows, err := tx.QueryContext(ctx, `SELECT pid, pid_start_time, token FROM agent_inbox_waiters WHERE agent_id=?`, agent)
	if err != nil {
		return fmt.Errorf("read inbox waiters for pruning: %w", err)
	}
	dead, err := s.collectDeadWaiters(rows)
	if err != nil {
		return err
	}
	for _, k := range dead {
		// Drop a waiter row whose process is gone.
		if _, err = tx.ExecContext(ctx, `DELETE FROM agent_inbox_waiters
			WHERE agent_id=? AND pid=? AND pid_start_time=? AND token=?`, agent, k.pid, k.start, k.token); err != nil {
			return fmt.Errorf("prune dead inbox waiter: %w", err)
		}
	}
	return nil
}

func containsPID(pids []int64, pid int64) bool {
	for _, p := range pids {
		if p == pid {
			return true
		}
	}
	return false
}

// collectDeadWaiters drains and closes rows, returning the waiters whose process
// is gone or has a different start time.
func (s *Store) collectDeadWaiters(rows *sql.Rows) ([]waiterKey, error) {
	defer func() { _ = rows.Close() }()
	var dead []waiterKey
	for rows.Next() {
		var k waiterKey
		if err := rows.Scan(&k.pid, &k.start, &k.token); err != nil {
			return nil, fmt.Errorf("scan inbox waiter: %w", err)
		}
		if !s.waiterAlive(int(k.pid), k.start) {
			dead = append(dead, k)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate inbox waiters: %w", err)
	}
	return dead, nil
}
