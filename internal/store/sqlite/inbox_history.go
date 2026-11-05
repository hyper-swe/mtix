// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// InboxState is where one addressed event stands for its recipient.
type InboxState string

const (
	// InboxUnacked: the recipient has not acknowledged the event.
	InboxUnacked InboxState = "unacked"
	// InboxAcked: acknowledged individually; the ledger holds the time.
	InboxAcked InboxState = "acked"
	// InboxAckedThrough: at or below the cumulative watermark, with no
	// per-event ledger row left (ack --through prunes them), so no time.
	InboxAckedThrough InboxState = "acked_through"
)

// InboxHistoryEntry is one event ever addressed to an agent.
type InboxHistoryEntry struct {
	Seq       int64      `json:"seq"`
	EventID   string     `json:"event_id"`
	NodeID    string     `json:"node_id"`
	Author    string     `json:"author"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"created_at"`
	State     InboxState `json:"state"`
	AckedAt   *time.Time `json:"acked_at,omitempty"`
}

// inboxHistorySQL lists every event ever addressed to the agent, whether or
// not it was acknowledged (FR-19.4): comments whose payload names the agent
// and hook deliveries, joined to the per-event ack ledger. State is derived:
// a ledger row is "acked" with its time; no row but a seq at or below the
// cumulative watermark is "acked_through" (ack --through pruned the row);
// anything else is "unacked". Read-only: no ack, cursor or session write.
const inboxHistorySQL = `WITH addressed(seq) AS (
 SELECT e.rowid FROM sync_events e
 WHERE e.op_type = 'comment'
 AND (CASE WHEN json_valid(e.payload) THEN json_extract(e.payload, '$.to') END) = ?
 UNION
 SELECT e.rowid FROM inbox_deliveries d
 JOIN sync_events e ON e.rowid = d.event_seq
 WHERE d.agent_id = ?
)
SELECT e.rowid, e.event_id, e.node_id, e.author_id, e.payload, e.created_at,
 k.acked_at,
 e.rowid <= COALESCE((SELECT cursor FROM agent_inbox_cursor WHERE agent_id = ?), 0)
FROM addressed a
JOIN sync_events e ON e.rowid = a.seq
LEFT JOIN agent_inbox_ack k ON k.agent_id = ? AND k.event_seq = a.seq
ORDER BY e.rowid ASC`

// InboxHistory returns every event addressed to agentID, oldest first, with
// its acknowledgement state and, for an individually acknowledged event, the
// time. It is the read-only audit of what was addressed to an agent and when
// it was acked (MTIX-112.2); it never acks, moves the cursor or refreshes a
// session. Events acked by ack --through carry no time: that ack prunes the
// per-event rows below the watermark.
func (s *Store) InboxHistory(ctx context.Context, agentID string) ([]InboxHistoryEntry, error) {
	if agentID == "" {
		return nil, fmt.Errorf("inbox history: agent id required: %w", model.ErrInvalidInput)
	}
	rows, err := s.readDB.QueryContext(ctx, inboxHistorySQL, agentID, agentID, agentID, agentID)
	if err != nil {
		return nil, fmt.Errorf("inbox history %q: %w", agentID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []InboxHistoryEntry
	for rows.Next() {
		entry, scanErr := scanHistoryRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inbox history iterate %q: %w", agentID, err)
	}
	return out, nil
}

func scanHistoryRow(rows *sql.Rows) (InboxHistoryEntry, error) {
	var (
		e                  InboxHistoryEntry
		payload, createdAt string
		ackedAt            sql.NullString
		belowWatermark     bool
	)
	if err := rows.Scan(&e.Seq, &e.EventID, &e.NodeID, &e.Author, &payload, &createdAt, &ackedAt, &belowWatermark); err != nil {
		return e, fmt.Errorf("inbox history scan: %w", err)
	}
	var p model.CommentPayload
	if json.Unmarshal([]byte(payload), &p) == nil {
		e.Body = p.Body
		if p.AuthorID != "" {
			e.Author = p.AuthorID
		}
	}
	e.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdAt)
	switch {
	case ackedAt.Valid:
		e.State = InboxAcked
		if t, err := time.Parse(time.RFC3339Nano, ackedAt.String); err == nil {
			e.AckedAt = &t
		}
	case belowWatermark:
		e.State = InboxAckedThrough
	default:
		e.State = InboxUnacked
	}
	return e, nil
}

// unackedInboxSQL counts the (agent, event) pairs still unacknowledged across
// every addressee: the events a destructive command would destroy unread.
const unackedInboxSQL = `WITH addressed(agent, seq) AS (
 SELECT CASE WHEN json_valid(e.payload) THEN json_extract(e.payload, '$.to') END, e.rowid
 FROM sync_events e WHERE e.op_type = 'comment'
 UNION
 SELECT d.agent_id, d.event_seq FROM inbox_deliveries d
 JOIN sync_events e ON e.rowid = d.event_seq
)
SELECT COUNT(*) FROM addressed a
WHERE a.agent IS NOT NULL AND a.agent <> ''
 AND a.seq > COALESCE((SELECT cursor FROM agent_inbox_cursor c WHERE c.agent_id = a.agent), 0)
 AND NOT EXISTS (SELECT 1 FROM agent_inbox_ack k WHERE k.agent_id = a.agent AND k.event_seq = a.seq)`

// UnackedInboxEvents returns how many events addressed to any agent are not
// yet acknowledged. The destructive commands print it before they run, so an
// operator sees the unread mail a wipe or replace would discard (MTIX-112.2).
func (s *Store) UnackedInboxEvents(ctx context.Context) (int64, error) {
	return countUnackedInbox(ctx, s.readDB)
}

// CountUnackedInboxFile is UnackedInboxEvents for a database file opened
// read-only, for recover, which must not open the damaged store as a Store.
// The file: scheme is what makes mode=ro count: a plain path with a query
// string is opened read-write and created when missing. A missing or empty
// file is an error, never a new database (MTIX-65, MTIX-112.2).
func CountUnackedInboxFile(ctx context.Context, dbPath string) (int64, error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		return 0, fmt.Errorf("count unacknowledged inbox events: %w", err)
	}
	if info.Size() == 0 {
		return 0, fmt.Errorf("count unacknowledged inbox events: %s is 0 bytes, not a database", dbPath)
	}
	n, err := countUnackedRO(ctx, dbPath, "mode=ro")
	if err != nil {
		// A WAL database needs a writable -shm to be read normally. When the
		// file or its directory is read-only, read the main file as it is
		// (immutable ignores an unmerged -wal, so the count can be low).
		var immErr error
		if n, immErr = countUnackedRO(ctx, dbPath, "mode=ro", "immutable=1"); immErr != nil {
			return 0, err
		}
	}
	return n, nil
}

// countUnackedRO opens path with the given URI parameters and counts; it never
// writes.
func countUnackedRO(ctx context.Context, path string, params ...string) (int64, error) {
	db, err := openSQLite(path, params...)
	if err != nil {
		return 0, fmt.Errorf("open read-only: %w", err)
	}
	defer func() { _ = db.Close() }()
	return countUnackedInbox(ctx, db)
}

// fileURIPath escapes the characters that end a file: URI path. A Windows
// drive path becomes /C:/dir/file, the form SQLite reads (MTIX-112.2.4).
func fileURIPath(path string) string {
	if vol := filepath.VolumeName(path); len(vol) == 2 && vol[1] == ':' {
		path = "/" + filepath.ToSlash(path)
	}
	return strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

func countUnackedInbox(ctx context.Context, db *sql.DB) (int64, error) {
	var n int64
	if err := db.QueryRowContext(ctx, unackedInboxSQL).Scan(&n); err != nil {
		return 0, fmt.Errorf("count unacknowledged inbox events: %w", err)
	}
	return n, nil
}
