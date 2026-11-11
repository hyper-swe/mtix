// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/procinfo"
)

const (
	// InboxReadThrottle is the shortest gap between two last-read writes for one
	// agent. An ack ignores it and always writes (MTIX-112.3).
	InboxReadThrottle = 30 * time.Second
	// MaxCursorMarks bounds the cumulative-ack marks kept per agent. When a new
	// mark would exceed it the lowest cursors are evicted first, and an event
	// covered only by an evicted mark reports its ack time as unknown.
	MaxCursorMarks = 256

	// deliveryTimeLayout is fixed width UTC, so stored times compare as text.
	deliveryTimeLayout = "2006-01-02T15:04:05.000000000Z"
	deliveryWriteBound = 5 * time.Second
)

// Ack kinds reported by InboxAckInfo.
const (
	AckNone      = "unacked"
	AckSelective = "selective"
	AckWatermark = "watermark"
)

// AckInfo is the local acknowledgement state of one event for one agent.
type AckInfo struct {
	Kind      string     // AckNone, AckSelective or AckWatermark
	AckedAt   *time.Time // nil when unacked or when the time is unknown
	TimeKnown bool       // false for a watermark ack no mark covers (older or evicted)
}

// Parked is the local wait state of one agent.
type Parked struct {
	Parked bool
	Since  *time.Time // earliest live waiter start
	PIDs   []int64
}

func deliveryTime(t time.Time) string { return t.UTC().Format(deliveryTimeLayout) }

// noIdentity reports a context built for internal reads (the wake scanner and
// re-evaluation attach an empty session identity): those reads are not the
// agent reading its inbox and must not stamp last read.
func noIdentity(ctx context.Context) bool {
	t, ok := ctx.Value(sessionTouchKey{}).(SessionTouch)
	return ok && t.Handle == ""
}

// stampRead records that agent read its inbox now, at most once per
// InboxReadThrottle. It is best effort: a refused or failed write is logged and
// never fails the read (MTIX-112.3). Parameterized SQL only.
func (s *Store) stampRead(ctx context.Context, agent string) {
	if noIdentity(ctx) || s.preflightWrite() != nil {
		return
	}
	now := s.clock()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryWriteBound)
	defer cancel()
	// Upsert the agent's last read; an existing row moves only when it is at
	// least the throttle old, so a busy poller does not write on every read.
	_, err := s.writeDB.ExecContext(wctx, `INSERT INTO agent_inbox_reads(agent_id,last_read_at) VALUES(?,?)
		ON CONFLICT(agent_id) DO UPDATE SET last_read_at=excluded.last_read_at
		WHERE agent_inbox_reads.last_read_at <= ?`,
		agent, deliveryTime(now), deliveryTime(now.Add(-InboxReadThrottle)))
	if err != nil {
		s.logger.Warn("record inbox last read", slog.String("agent_id", agent), slog.Any("error", err))
	}
}

// writeAckRead records last read inside the ack transaction; unlike stampRead it
// is unconditional (an ack is proof of a read).
func (s *Store) writeAckRead(ctx context.Context, tx *sql.Tx, agent string) error {
	// An ack always moves the agent's last read to now.
	_, err := tx.ExecContext(ctx, `INSERT INTO agent_inbox_reads(agent_id,last_read_at) VALUES(?,?)
		ON CONFLICT(agent_id) DO UPDATE SET last_read_at=excluded.last_read_at`,
		agent, deliveryTime(s.clock()))
	if err != nil {
		return fmt.Errorf("record last read on ack: %w", err)
	}
	return nil
}

// writeCursorMark records the watermark's new position and evicts the lowest
// cursors beyond MaxCursorMarks, all inside the ack transaction.
func (s *Store) writeCursorMark(ctx context.Context, tx *sql.Tx, agent string) error {
	// Mark the resulting watermark; OR IGNORE keeps the first time a cursor was reached.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_inbox_cursor_marks(agent_id,cursor,advanced_at)
		SELECT agent_id, cursor, ? FROM agent_inbox_cursor WHERE agent_id=?`,
		deliveryTime(s.clock()), agent); err != nil {
		return fmt.Errorf("record cursor mark: %w", err)
	}
	// Keep only the newest MaxCursorMarks marks of this agent (oldest cursors go first).
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_inbox_cursor_marks WHERE agent_id=? AND cursor NOT IN (
		SELECT cursor FROM agent_inbox_cursor_marks WHERE agent_id=? ORDER BY cursor DESC LIMIT ?)`,
		agent, agent, MaxCursorMarks); err != nil {
		return fmt.Errorf("evict old cursor marks: %w", err)
	}
	return nil
}

// InboxAckInfo reports whether agent acked the event seq, when and how. It reads
// local state only and writes nothing.
func (s *Store) InboxAckInfo(ctx context.Context, agent string, seq int64) (AckInfo, error) {
	if agent == "" {
		return AckInfo{}, fmt.Errorf("inbox ack info: agent id required: %w", model.ErrInvalidInput)
	}
	var at string
	// A per-event ack row names its own time.
	err := s.readDB.QueryRowContext(ctx, `SELECT acked_at FROM agent_inbox_ack WHERE agent_id=? AND event_seq=?`,
		agent, seq).Scan(&at)
	switch {
	case err == nil:
		return ackInfoAt(AckSelective, at), nil
	case !errors.Is(err, sql.ErrNoRows):
		return AckInfo{}, fmt.Errorf("read selective ack: %w", err)
	}
	var cursor int64
	// The watermark covers every event at or below the agent's cursor.
	err = s.readDB.QueryRowContext(ctx, `SELECT cursor FROM agent_inbox_cursor WHERE agent_id=?`, agent).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && seq > cursor) {
		return AckInfo{Kind: AckNone}, nil
	}
	if err != nil {
		return AckInfo{}, fmt.Errorf("read ack watermark: %w", err)
	}
	// The first mark at or above seq is when the watermark first covered it.
	err = s.readDB.QueryRowContext(ctx, `SELECT advanced_at FROM agent_inbox_cursor_marks
		WHERE agent_id=? AND cursor>=? ORDER BY cursor ASC LIMIT 1`, agent, seq).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return AckInfo{Kind: AckWatermark}, nil
	}
	if err != nil {
		return AckInfo{}, fmt.Errorf("read cursor mark: %w", err)
	}
	return ackInfoAt(AckWatermark, at), nil
}

func ackInfoAt(kind, at string) AckInfo {
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return AckInfo{Kind: kind}
	}
	return AckInfo{Kind: kind, AckedAt: &t, TimeKnown: true}
}

// InboxLastRead returns when agent last read its inbox, or nil if never.
func (s *Store) InboxLastRead(ctx context.Context, agent string) (*time.Time, error) {
	var at string
	// Last read comes from the throttled stamp written by list, wait and ack.
	err := s.readDB.QueryRowContext(ctx, `SELECT last_read_at FROM agent_inbox_reads WHERE agent_id=?`, agent).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // nil means the agent never read
	}
	if err != nil {
		return nil, fmt.Errorf("read last read: %w", err)
	}
	t, perr := time.Parse(time.RFC3339Nano, at)
	if perr != nil {
		return nil, fmt.Errorf("parse last read %q: %w", at, perr)
	}
	return &t, nil
}

// InboxParked reports whether agent has a live wait on this machine. A waiter
// row counts only when its pid is running with the recorded start time, so a
// crashed or pid-reused waiter is never reported parked. It writes nothing.
func (s *Store) InboxParked(ctx context.Context, agent string) (Parked, error) {
	// Candidate waiters for the agent, earliest first.
	rows, err := s.readDB.QueryContext(ctx, `SELECT pid, pid_start_time, since FROM agent_inbox_waiters
		WHERE agent_id=? ORDER BY since ASC, pid ASC`, agent)
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
		out.PIDs = append(out.PIDs, pid)
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

// registerWaiter records a live wait and returns the function that removes it.
// Dead rows of the same agent are pruned here, not by a timer. Best effort: a
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
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryWriteBound)
	defer cancel()
	s.pruneDeadWaiters(wctx, agent)
	// Record this wait; a repeated start by the same process refreshes the row.
	if _, err := s.writeDB.ExecContext(wctx, `INSERT OR REPLACE INTO agent_inbox_waiters(agent_id,pid,pid_start_time,since)
		VALUES(?,?,?,?)`, agent, pid, start, deliveryTime(s.clock())); err != nil {
		s.logger.Warn("record inbox waiter", slog.String("agent_id", agent), slog.Any("error", err))
		return noop
	}
	return func() { s.removeWaiter(ctx, agent, pid, start) }
}

func (s *Store) removeWaiter(ctx context.Context, agent string, pid int, start string) {
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryWriteBound)
	defer cancel()
	// Remove exactly this wait on every exit path.
	if _, err := s.writeDB.ExecContext(dctx, `DELETE FROM agent_inbox_waiters WHERE agent_id=? AND pid=? AND pid_start_time=?`,
		agent, pid, start); err != nil {
		s.logger.Warn("remove inbox waiter", slog.String("agent_id", agent), slog.Any("error", err))
	}
}

func (s *Store) pruneDeadWaiters(ctx context.Context, agent string) {
	// Read the agent's rows first: liveness is probed outside SQL.
	rows, err := s.writeDB.QueryContext(ctx, `SELECT pid, pid_start_time FROM agent_inbox_waiters WHERE agent_id=?`, agent)
	if err != nil {
		s.logger.Warn("read inbox waiters for pruning", slog.String("agent_id", agent), slog.Any("error", err))
		return
	}
	dead := s.deadWaiters(rows)
	for _, k := range dead {
		// Drop a waiter row whose process is gone.
		if _, derr := s.writeDB.ExecContext(ctx, `DELETE FROM agent_inbox_waiters WHERE agent_id=? AND pid=? AND pid_start_time=?`,
			agent, k.pid, k.start); derr != nil {
			s.logger.Warn("prune dead inbox waiter", slog.String("agent_id", agent), slog.Any("error", derr))
		}
	}
}

type waiterKey struct {
	pid   int64
	start string
}

// deadWaiters scans the rows and closes them; it returns the waiters whose
// process is gone or has a different start time.
func (s *Store) deadWaiters(rows *sql.Rows) []waiterKey {
	defer func() { _ = rows.Close() }()
	var dead []waiterKey
	for rows.Next() {
		var k waiterKey
		if rows.Scan(&k.pid, &k.start) == nil && !s.waiterAlive(int(k.pid), k.start) {
			dead = append(dead, k)
		}
	}
	return dead
}
