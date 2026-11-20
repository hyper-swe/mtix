// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// exportTokenRevision is bumped whenever the shape of an export changes, so a
// token recorded by an older shape never authorizes skipping a publication
// (MTIX-130.20.2).
const exportTokenRevision = 1

// publishedExportKey is the meta key of the record of the last publication.
const publishedExportKey = "export.published_state"

// exportedTables names every table the export snapshot reads: the one list the
// change triggers in exportGenerationSQL and the export readers are both
// checked against (export_generation_drift_test.go). A table added to the
// export must be added here and get its three triggers, or that test fails
// (MTIX-130.20.2).
func exportedTables() []string {
	return []string{"agents", "dependencies", "nodes", "sessions", "task_history"}
}

// exportGenerationSQL creates the change counter of exported content and the
// triggers that bump it (MTIX-130.20.2, FR-15.3). Every INSERT, UPDATE and
// DELETE on a table the export carries (nodes, dependencies, agents, sessions,
// task_history) adds one to gen in the writing transaction, so a committed
// write always moves it and a rolled-back one never does. The triggers live in
// the database file, so every writer fires them: this build, an older build,
// import and restore paths, a manual sqlite3 session. Tables the export does
// not carry (meta, sequences, relay, inbox, leases) have no trigger, so the
// bookkeeping of the export itself never counts as a board change. A same-value
// UPDATE bumps too: that only costs one extra full pass, never a missed one.
// Constant DDL only; no statement is built from a value.
const exportGenerationSQL = `
CREATE TABLE IF NOT EXISTS export_generation (
    id  INTEGER PRIMARY KEY CHECK (id = 1),
    gen INTEGER NOT NULL
);
INSERT OR IGNORE INTO export_generation (id, gen) VALUES (1, 0);

CREATE TRIGGER IF NOT EXISTS export_gen_nodes_ai AFTER INSERT ON nodes BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_nodes_au AFTER UPDATE ON nodes BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_nodes_ad AFTER DELETE ON nodes BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_dependencies_ai AFTER INSERT ON dependencies BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_dependencies_au AFTER UPDATE ON dependencies BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_dependencies_ad AFTER DELETE ON dependencies BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_agents_ai AFTER INSERT ON agents BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_agents_au AFTER UPDATE ON agents BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_agents_ad AFTER DELETE ON agents BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_sessions_ai AFTER INSERT ON sessions BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_sessions_au AFTER UPDATE ON sessions BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_sessions_ad AFTER DELETE ON sessions BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_task_history_ai AFTER INSERT ON task_history BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_task_history_au AFTER UPDATE ON task_history BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
CREATE TRIGGER IF NOT EXISTS export_gen_task_history_ad AFTER DELETE ON task_history BEGIN UPDATE export_generation SET gen = gen + 1 WHERE id = 1; END;
`

// ExportChangeToken names the state of the exported content: the change
// counter, whether history is exported and the export shape revision. Two
// equal tokens read from the same database mean no exported content was
// written between them. Callers read it BEFORE Export, so a write that lands
// after the read always yields a different token than the one recorded
// (MTIX-130.20.2). An error means "unknown": the caller must do the full pass.
func (s *Store) ExportChangeToken(ctx context.Context) (string, error) {
	var gen int64
	if err := s.readDB.QueryRowContext(ctx, `SELECT gen FROM export_generation WHERE id = 1`).Scan(&gen); err != nil {
		return "", fmt.Errorf("read export change counter: %w", err)
	}
	history := 0
	if s.historyExportOn() {
		history = 1
	}
	return fmt.Sprintf("%d|h%d|r%d", gen, history, exportTokenRevision), nil
}

// PublishedExport records what the last publication left behind, so a later
// pass can prove nothing changed without reading the board (MTIX-130.20.2).
type PublishedExport struct {
	Version  int    `json:"v"`
	Token    string `json:"token"`
	Build    string `json:"build"`
	FileHash string `json:"file_hash"`
	DBHash   string `json:"db_hash"`
	Size     int64  `json:"size"`
	MtimeNS  int64  `json:"mtime_ns"`
	// CtimeNS, Ino and Dev identify the published file beyond its size and
	// modification time: a user cannot set the change time, and a replacement
	// by rename is another inode. All zero where the platform has none.
	CtimeNS int64  `json:"ctime_ns"`
	Ino     uint64 `json:"ino"`
	Dev     uint64 `json:"dev"`
}

// PublishedExportVersion is the record format this build reads and writes.
// Revision 2 added the file identity (change time, inode, device): a record
// written by an earlier build never matches and forces one full pass.
const PublishedExportVersion = 2

// ReadPublishedExport returns the record of the last publication; ok is false
// when there is none or it cannot be trusted (absent, malformed, other version).
func (s *Store) ReadPublishedExport(ctx context.Context) (rec PublishedExport, ok bool, err error) {
	var raw string
	err = s.readDB.QueryRowContext(ctx, "SELECT value FROM meta WHERE key = ?", publishedExportKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return PublishedExport{}, false, nil
	}
	if err != nil {
		return PublishedExport{}, false, fmt.Errorf("read published export record: %w", err)
	}
	if json.Unmarshal([]byte(raw), &rec) != nil || rec.Version != PublishedExportVersion {
		return PublishedExport{}, false, nil
	}
	return rec, true, nil
}

// WritePublishedExport stores the record after a publication (or after a pass
// proved the published file current). meta is not an exported table, so this
// never moves the change counter.
func (s *Store) WritePublishedExport(ctx context.Context, rec PublishedExport) error {
	rec.Version = PublishedExportVersion
	body, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode published export record: %w", err)
	}
	return s.withWriteTx(ctx, func(tx *sql.Tx) error {
		// Keep the one record of the last publication: replaced whole.
		if _, execErr := tx.ExecContext(ctx, "INSERT INTO meta (key,value) VALUES (?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", publishedExportKey, string(body)); execErr != nil {
			return fmt.Errorf("write published export record: %w", execErr)
		}
		return nil
	}, false)
}

// clearPublishedExportTx drops the record inside the caller's transaction, so
// an import or restore invalidates the skip with the change itself.
func clearPublishedExportTx(ctx context.Context, tx *sql.Tx) error {
	// Forget the last publication: the board is being replaced.
	if _, err := tx.ExecContext(ctx, "DELETE FROM meta WHERE key = ?", publishedExportKey); err != nil {
		return fmt.Errorf("clear published export record: %w", err)
	}
	return nil
}
