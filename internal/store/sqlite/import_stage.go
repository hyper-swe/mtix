// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// Bounded-memory import, slice 1 (MTIX-2.3.2.1). The JSON export is
// token-streamed: each element of the nodes, dependencies and history arrays
// is decoded one at a time and written, as its raw JSON text, to an indexed
// SQLite staging database on disk. Memory use is bounded by the largest single
// element, never by the board. The wire format is unchanged: StageExport
// accepts exactly the files DecodeExportData accepts.

// stageKind names one staged array of the export.
type stageKind int

const (
	stageNodes stageKind = iota
	stageDeps
	stageHistory
	stageKindCount
)

// arrayPresence records how the file spelled an array, because the checksum
// tells apart a missing or null array (hashed as null) from an empty one.
type arrayPresence int

const (
	arrayAbsent arrayPresence = iota // key missing or null: decodes as a nil slice
	arrayEmpty                       // []
	arrayFilled
)

const stageSchemaSQL = `
CREATE TABLE stage_rows (
	kind INTEGER NOT NULL, ord INTEGER NOT NULL,
	k1 TEXT NOT NULL, k2 TEXT NOT NULL, body BLOB NOT NULL,
	PRIMARY KEY (kind, ord)) WITHOUT ROWID;
CREATE INDEX stage_rows_key ON stage_rows (kind, k1, k2);
CREATE TABLE stage_ids (
	src INTEGER NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL, hex TEXT NOT NULL);`

// StagedExport is an export file staged in a disk-backed SQLite database.
// Header holds every top-level field except the three staged arrays.
type StagedExport struct {
	// Header carries the scalar fields, agents, sessions and inbox journal.
	// Its Nodes, Dependencies and History stay nil: the rows live on disk.
	Header ExportData

	db       *sql.DB
	dir      string
	ownsDir  bool
	counts   [stageKindCount]int
	presence [stageKindCount]arrayPresence
}

// NodeCount returns the number of staged nodes.
func (st *StagedExport) NodeCount() int { return st.counts[stageNodes] }

// Close releases the staging database and deletes its files.
func (st *StagedExport) Close() error {
	err := st.db.Close()
	for _, f := range []string{"stage.db", "stage.db-journal", "stage.db-wal", "stage.db-shm"} {
		// #nosec G703 -- st.dir is the caller-chosen or private temp staging directory; the file names are fixed and no board content selects them.
		if rmErr := os.Remove(filepath.Join(st.dir, f)); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) && err == nil {
			err = fmt.Errorf("remove staging file: %w", rmErr)
		}
	}
	if st.ownsDir {
		// #nosec G703 -- st.dir was created by openStage with os.MkdirTemp; no board content selects it.
		if rmErr := os.Remove(st.dir); rmErr != nil && err == nil {
			err = fmt.Errorf("remove staging dir: %w", rmErr)
		}
	}
	return err
}

// StageExport stream-decodes the export read from r into a staging database
// created under dir (a private temporary directory when dir is empty). It
// rejects trailing content like DecodeExportData. The caller must Close the
// result.
func StageExport(ctx context.Context, r io.Reader, dir string) (*StagedExport, error) {
	st, err := openStage(dir)
	if err != nil {
		return nil, err
	}
	if err := st.stage(ctx, json.NewDecoder(bufio.NewReader(r))); err != nil {
		if closeErr := st.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, err
	}
	return st, nil
}

func openStage(dir string) (*StagedExport, error) {
	owns := dir == ""
	if owns {
		made, err := os.MkdirTemp("", "mtix-import-stage-")
		if err != nil {
			return nil, fmt.Errorf("create staging dir: %w", err)
		}
		dir = made
	}
	db, err := openSQLite(filepath.Join(dir, "stage.db"),
		"_pragma=journal_mode(OFF)", "_pragma=synchronous(OFF)", "_pragma=cache_size(-2000)")
	if err != nil {
		return nil, fmt.Errorf("open staging database: %w", err)
	}
	db.SetMaxOpenConns(1)
	st := &StagedExport{db: db, dir: dir, ownsDir: owns}
	if _, err := db.ExecContext(context.Background(), stageSchemaSQL); err != nil {
		if closeErr := st.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, fmt.Errorf("create staging tables: %w", err)
	}
	return st, nil
}

// stage reads the whole export object inside one staging transaction.
func (st *StagedExport) stage(ctx context.Context, dec *json.Decoder) error {
	tx, err := st.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin staging: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit
	if err := st.readObject(ctx, tx, dec); err != nil {
		return fmt.Errorf("parse export data: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("parse export data: trailing content after export object: %w", err)
		}
		return fmt.Errorf("unexpected trailing content after export object: %w", model.ErrInvalidInput)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit staging: %w", err)
	}
	return nil
}

// readObject streams the top-level object. A top-level null decodes as an
// empty export, as json.Unmarshal does.
func (st *StagedExport) readObject(ctx context.Context, tx *sql.Tx, dec *json.Decoder) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if tok == nil {
		return nil
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("export is not a JSON object: %w", model.ErrInvalidInput)
	}
	for dec.More() {
		keyTok, keyErr := dec.Token()
		if keyErr != nil {
			return keyErr
		}
		key, _ := keyTok.(string)
		if fieldErr := st.readField(ctx, tx, dec, key); fieldErr != nil {
			return fieldErr
		}
	}
	_, err = dec.Token() // the closing brace
	return err
}

// arrayKinds maps the staged array keys to their kind; encoding/json matches
// keys case-insensitively, so does the staging reader.
var arrayKinds = []struct {
	key  string
	kind stageKind
}{{"nodes", stageNodes}, {"dependencies", stageDeps}, {"history", stageHistory}}

func (st *StagedExport) readField(ctx context.Context, tx *sql.Tx, dec *json.Decoder, key string) error {
	for _, a := range arrayKinds {
		if strings.EqualFold(key, a.key) {
			return st.stageArray(ctx, tx, dec, a.kind)
		}
	}
	if dest := st.headerField(key); dest != nil {
		return dec.Decode(dest)
	}
	var skip json.RawMessage
	return dec.Decode(&skip)
}

// headerField returns the destination of a non-array top-level field, or nil.
func (st *StagedExport) headerField(key string) any {
	h := &st.Header
	fields := []struct {
		name string
		dest any
	}{{"inbox_journal", &h.InboxJournal}, {"version", &h.Version},
		{"schema_version", &h.SchemaVersion}, {"exported_at", &h.ExportedAt},
		{"mtix_version", &h.MtixVersion}, {"project", &h.Project},
		{"agents", &h.Agents}, {"sessions", &h.Sessions},
		{"node_count", &h.NodeCount}, {"checksum", &h.Checksum},
		{"ids_checksum", &h.IDsChecksum}}
	for _, f := range fields {
		if strings.EqualFold(key, f.name) {
			return f.dest
		}
	}
	return nil
}
