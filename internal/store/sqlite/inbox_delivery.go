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

	"github.com/hyper-swe/mtix/internal/model"
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

type noReadStampKey struct{}

// WithoutReadStamp marks ctx as an internal read (the wake scanner and its
// re-evaluation): reading an inbox for those is not the agent reading it, so it
// must not stamp last read (MTIX-112.3).
func WithoutReadStamp(ctx context.Context) context.Context {
	return context.WithValue(ctx, noReadStampKey{}, true)
}

func readStampSuppressed(ctx context.Context) bool {
	v, _ := ctx.Value(noReadStampKey{}).(bool)
	return v
}

// stampRead records that agent read its inbox now, at most once per
// InboxReadThrottle. It is best effort: a refused or failed write is logged and
// never fails the read (MTIX-112.3). Parameterized SQL only.
func (s *Store) stampRead(ctx context.Context, agent string) {
	if readStampSuppressed(ctx) || s.preflightWrite() != nil {
		return
	}
	now := s.clock()
	if !s.readStampDue(ctx, agent, now) {
		return // throttled: no writer transaction for a stamp that would be refused
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryWriteBound)
	defer cancel()
	// Local bookkeeping: no export or hook callbacks (notify=false).
	err := s.withWriteTx(wctx, func(tx *sql.Tx) error {
		// Upsert the agent's last read; an existing row moves only when it is at
		// least the throttle old, so a busy poller does not write on every read.
		_, execErr := tx.ExecContext(wctx, `INSERT INTO agent_inbox_reads(agent_id,last_read_at) VALUES(?,?)
			ON CONFLICT(agent_id) DO UPDATE SET last_read_at=excluded.last_read_at
			WHERE agent_inbox_reads.last_read_at <= ?`,
			agent, deliveryTime(now), deliveryTime(now.Add(-InboxReadThrottle)))
		return execErr
	}, false)
	if err != nil {
		s.logger.Warn("record inbox last read", slog.String("agent_id", agent), slog.Any("error", err))
	}
}

// readStampDue reports whether agent's last read is at least InboxReadThrottle
// old, using the read pool so a throttled list never queues on the writer. On a
// read error it answers true: the write's own condition still decides.
func (s *Store) readStampDue(ctx context.Context, agent string, now time.Time) bool {
	var fresh int
	// A row newer than the cutoff means the upsert below would change nothing.
	err := s.readDB.QueryRowContext(ctx, `SELECT 1 FROM agent_inbox_reads WHERE agent_id=? AND last_read_at > ?`,
		agent, deliveryTime(now.Add(-InboxReadThrottle))).Scan(&fresh)
	return err != nil
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

// writeCursorMark records that the watermark moved from `from` to `to` in this
// transaction, then applies the per-agent bound.
func (s *Store) writeCursorMark(ctx context.Context, tx *sql.Tx, agent string, from, to int64) error {
	// A watermark mark covers exactly the events in (from, to]; anything at or
	// below from was acked earlier and has no mark of its own.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_inbox_cursor_marks(agent_id,cursor,from_cursor,kind,advanced_at)
		VALUES(?,?,?,?,?)`, agent, to, from, AckWatermark, deliveryTime(s.clock())); err != nil {
		return fmt.Errorf("record cursor mark: %w", err)
	}
	return s.evictCursorMarks(ctx, tx, agent)
}

// keepFoldedSelectiveAcks keeps the kind and time of selective acks the
// watermark now covers, as single-event marks, before their ack rows are pruned.
func (s *Store) keepFoldedSelectiveAcks(ctx context.Context, tx *sql.Tx, agent string) error {
	// Each covered selective ack becomes a (seq-1, seq] mark with its own time;
	// OR IGNORE keeps the first time if the same event was acked and folded before.
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_inbox_cursor_marks(agent_id,cursor,from_cursor,kind,advanced_at)
		SELECT agent_id, event_seq, event_seq-1, ?, acked_at FROM agent_inbox_ack
		WHERE agent_id=? AND event_seq<=(SELECT cursor FROM agent_inbox_cursor WHERE agent_id=?)`,
		AckSelective, agent, agent); err != nil {
		return fmt.Errorf("keep folded selective acks: %w", err)
	}
	return nil
}

// evictCursorMarks keeps the newest MaxCursorMarks marks of agent. "Oldest" means
// the lowest cursor (the earliest event), whatever the mark's kind or when it was
// written, so a folded selective mark and a watermark mark compete on cursor alone
// and the lower one goes first. Marks can share a cursor (a folded single-event
// selective mark and a watermark ending at the same event): the one with the lower
// from_cursor (the earlier-starting coverage) goes first, then by kind. An event covered only by an evicted mark reports
// unknown.
func (s *Store) evictCursorMarks(ctx context.Context, tx *sql.Tx, agent string) error {
	// Rank by cursor, then range start, so the oldest coverage goes first.
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_inbox_cursor_marks WHERE agent_id=? AND rowid NOT IN (
		SELECT rowid FROM agent_inbox_cursor_marks WHERE agent_id=?
		ORDER BY cursor DESC, from_cursor DESC, kind ASC LIMIT ?)`, agent, agent, MaxCursorMarks); err != nil {
		return fmt.Errorf("evict old cursor marks: %w", err)
	}
	return nil
}

// tableExists reports whether the named table is in the database. A released
// 0.5.4 store opened read-only has none of the delivery tables and cannot
// create them, so the readers treat a missing table as empty (MTIX-112.3).
func tableExists(ctx context.Context, q rowQuerier, name string) (bool, error) {
	var present bool
	// Look the table up in the schema catalog, never by reading it.
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=?)`,
		name).Scan(&present); err != nil {
		return false, fmt.Errorf("inspect table %s: %w", name, err)
	}
	return present, nil
}

// InboxAckInfo reports whether agent acked the event seq, when and how. It reads
// local state only and writes nothing. Its queries share one read snapshot, so a
// concurrent ack, trim or discard cannot mix two states (MTIX-112.3).
func (s *Store) InboxAckInfo(ctx context.Context, agent string, seq int64) (AckInfo, error) {
	return ackInfoSnapshot(ctx, s.readDB, agent, seq, nil)
}

// ackInfoSnapshot runs ackInfoIn inside one read-only snapshot of db. wrap, when
// non-nil, decorates the snapshot's reader; production passes nil and a test
// uses it to interleave a write between two reads without a hook in the store.
func ackInfoSnapshot(ctx context.Context, db *sql.DB, agent string, seq int64, wrap func(rowQuerier) rowQuerier) (info AckInfo, err error) {
	if agent == "" {
		return AckInfo{}, fmt.Errorf("inbox ack info: agent id required: %w", model.ErrInvalidInput)
	}
	// The reader pool begins deferred; a WAL snapshot never takes writer ownership.
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return AckInfo{}, fmt.Errorf("begin ack info snapshot: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			info = AckInfo{}
			err = errors.Join(err, fmt.Errorf("rollback ack info snapshot: %w", rbErr))
		}
	}()
	var q rowQuerier = tx
	if wrap != nil {
		q = wrap(tx)
	}
	return ackInfoIn(ctx, q, agent, seq)
}

// ackInfoIn reads only through q, the caller's snapshot, so every query sees one state (MTIX-112.3).
func ackInfoIn(ctx context.Context, q rowQuerier, agent string, seq int64) (AckInfo, error) {
	var at string
	// A per-event ack row names its own time.
	err := q.QueryRowContext(ctx, `SELECT acked_at FROM agent_inbox_ack WHERE agent_id=? AND event_seq=?`,
		agent, seq).Scan(&at)
	switch {
	case err == nil && at == "":
		// Restore and carry write no time for a watermark-covered ack.
		return AckInfo{Kind: AckWatermark}, nil
	case err == nil:
		return ackInfoAt(AckSelective, at), nil
	case !errors.Is(err, sql.ErrNoRows):
		return AckInfo{}, fmt.Errorf("read selective ack: %w", err)
	}
	var cursor int64
	// The watermark covers every event at or below the agent's cursor.
	err = q.QueryRowContext(ctx, `SELECT cursor FROM agent_inbox_cursor WHERE agent_id=?`, agent).Scan(&cursor)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && seq > cursor) {
		return AckInfo{Kind: AckNone}, nil
	}
	if err != nil {
		return AckInfo{}, fmt.Errorf("read ack watermark: %w", err)
	}
	return coveringMarkInfo(ctx, q, agent, seq)
}

// coveringMarkInfo names the ack time of a watermark-covered event. Only a mark
// that covers seq (from_cursor < seq <= cursor) names its time; a selective mark
// wins over the watermark mark around it. No covering mark (older, evicted, or a
// store without the marks table) means the time is unknown, never a later
// mark's time.
func coveringMarkInfo(ctx context.Context, q rowQuerier, agent string, seq int64) (AckInfo, error) {
	present, err := tableExists(ctx, q, "agent_inbox_cursor_marks")
	if err != nil {
		return AckInfo{}, err
	}
	if !present {
		return AckInfo{Kind: AckWatermark}, nil
	}
	var kind, at string
	err = q.QueryRowContext(ctx, `SELECT kind, advanced_at FROM agent_inbox_cursor_marks
		WHERE agent_id=? AND from_cursor<? AND cursor>=?
		ORDER BY (kind=?) DESC, cursor ASC LIMIT 1`, agent, seq, seq, AckSelective).Scan(&kind, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return AckInfo{Kind: AckWatermark}, nil
	}
	if err != nil {
		return AckInfo{}, fmt.Errorf("read cursor mark: %w", err)
	}
	return ackInfoAt(kind, at), nil
}

// ackInfoAt builds the info of an ack of the given kind stamped at. With no
// recorded time the kind stays and the time is unknown: neither a made-up time
// nor a made-up kind (MTIX-112.3).
func ackInfoAt(kind, at string) AckInfo {
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return AckInfo{Kind: kind}
	}
	return AckInfo{Kind: kind, AckedAt: &t, TimeKnown: true}
}

// InboxLastRead returns when agent last read its inbox, or nil if never.
func (s *Store) InboxLastRead(ctx context.Context, agent string) (*time.Time, error) {
	present, terr := tableExists(ctx, s.readDB, "agent_inbox_reads")
	if terr != nil {
		return nil, terr
	}
	if !present {
		return nil, nil //nolint:nilnil // a store without the table was never read through this feature
	}
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
