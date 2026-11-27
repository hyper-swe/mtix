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

// Bounded-memory import, slice 1 (MTIX-2.3.2.1, FR-7.8, FR-7.8a). The JSON
// export is token-streamed: each element of the nodes, dependencies and
// history arrays is decoded one at a time and written, as its JSON text, to an
// indexed SQLite staging database on disk. Memory use is bounded by the
// largest single element, never by the board. The wire format is unchanged:
// StageExport accepts exactly the files DecodeExportData accepts, including
// the way encoding/json decodes a repeated array key (see stageArray; only a
// repeated key is decoded in memory).

// stageKind names one staged array of the export.
type stageKind int

const (
	stageNodes stageKind = iota
	stageDeps
	stageHistory
	stageKindCount
)

// String names the array for error messages.
func (k stageKind) String() string {
	switch k {
	case stageNodes:
		return "nodes"
	case stageDeps:
		return "dependencies"
	case stageHistory:
		return "history"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// arrayKind maps a top-level key to the array it names. encoding/json matches
// keys case-insensitively, so does the staging reader.
func arrayKind(key string) (stageKind, bool) {
	for k := stageKind(0); k < stageKindCount; k++ {
		if strings.EqualFold(key, k.String()) {
			return k, true
		}
	}
	return 0, false
}

// arrayPresence records how the file spelled an array, because the checksum
// tells apart a missing or null array (hashed as null) from an empty one.
type arrayPresence int

const (
	arrayAbsent arrayPresence = iota // key missing or null: decodes as a nil slice
	arrayEmpty                       // []
	arrayFilled
)

// stageSchemaSQL creates the staging tables. stage_rows holds one element per
// (kind, ord), ord being its place in the array; k1..k3 are its sort keys
// (node id; dependency from and to; history task uid, time and id), indexed so
// the rows can be read back in the canonical checksum order. stage_ids holds
// the non-UTF-8 id tuples of the elements of array src for ids_checksum.
const stageSchemaSQL = `
CREATE TABLE stage_rows (
	kind INTEGER NOT NULL, ord INTEGER NOT NULL,
	k1 TEXT NOT NULL, k2 TEXT NOT NULL, k3 TEXT NOT NULL, body BLOB NOT NULL,
	PRIMARY KEY (kind, ord)) WITHOUT ROWID;
CREATE INDEX stage_rows_key ON stage_rows (kind, k1, k2, k3);
CREATE TABLE stage_ids (
	src INTEGER NOT NULL, kind TEXT NOT NULL, key TEXT NOT NULL, hex TEXT NOT NULL);`

// StagedExport is an export file staged in a disk-backed SQLite database.
// Header holds every top-level field except the three staged arrays.
type StagedExport struct {
	// Header carries the scalar fields, agents, sessions and inbox journal.
	// Its Nodes, Dependencies and History stay nil: the rows live on disk.
	Header ExportData

	db       *sql.DB
	dir      string // private directory made by openStage and removed by Close
	counts   [stageKindCount]int
	presence [stageKindCount]arrayPresence
	seen     [stageKindCount]bool // the array key has occurred in the file
	replay   [stageKindCount]any  // *[]T decoded in memory, for a repeated array key
}

// NodeCount returns the number of staged nodes.
func (st *StagedExport) NodeCount() int { return st.counts[stageNodes] }

// Close releases the staging database and deletes its files and the private
// directory that held them.
func (st *StagedExport) Close() error {
	err := st.db.Close()
	if rmErr := removeStageDir(st.dir); rmErr != nil && err == nil {
		err = rmErr
	}
	return err
}

// removeStageDir deletes the staging files and then the (now empty) private
// staging directory.
func removeStageDir(dir string) error {
	for _, f := range []string{"stage.db", "stage.db-journal", "stage.db-wal", "stage.db-shm"} {
		// #nosec G703 -- dir is the private directory openStage made with os.MkdirTemp; the file names are fixed and no board content selects them.
		if err := os.Remove(filepath.Join(dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove staging file: %w", err)
		}
	}
	// #nosec G703 -- dir was created by openStage with os.MkdirTemp; no board content selects it.
	if err := os.Remove(dir); err != nil {
		return fmt.Errorf("remove staging dir: %w", err)
	}
	return nil
}

// StageExport stream-decodes the export read from r into a staging database
// held in a fresh private directory created under dir (the system temporary
// directory when dir is empty), per FR-7.8 and FR-7.8a. It rejects trailing
// content like DecodeExportData. The caller must Close the result, which
// removes the private directory; a file already in dir is never opened or
// removed.
func StageExport(ctx context.Context, r io.Reader, dir string) (*StagedExport, error) {
	st, err := openStage(ctx, dir)
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

// openStage creates the private staging directory and database, and removes
// the directory again when the database cannot be opened or initialised.
func openStage(ctx context.Context, dir string) (*StagedExport, error) {
	made, err := os.MkdirTemp(dir, "mtix-import-stage-")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	db, err := openShieldedSQLite(filepath.Join(made, "stage.db"),
		"_pragma=journal_mode(OFF)", "_pragma=synchronous(OFF)", "_pragma=cache_size(-2000)")
	if err != nil {
		err = fmt.Errorf("open staging database: %w", err)
		if rmErr := removeStageDir(made); rmErr != nil {
			err = errors.Join(err, rmErr)
		}
		return nil, err
	}
	db.SetMaxOpenConns(1)
	st := &StagedExport{db: db, dir: made}
	if _, err := db.ExecContext(ctx, stageSchemaSQL); err != nil {
		err = fmt.Errorf("create staging tables: %w", err)
		if closeErr := st.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, err
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
		return fmt.Errorf("read export object: %w", err)
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
			return fmt.Errorf("read export key: %w", keyErr)
		}
		key, _ := keyTok.(string)
		if fieldErr := st.readField(ctx, tx, dec, key); fieldErr != nil {
			return fieldErr
		}
	}
	if _, err = dec.Token(); err != nil { // the closing brace
		return fmt.Errorf("read export object end: %w", err)
	}
	return st.restageRepeated(ctx, tx)
}

// readField reads the value of one top-level key into the staging database or
// the header, or skips it when the export format does not know it.
func (st *StagedExport) readField(ctx context.Context, tx *sql.Tx, dec *json.Decoder, key string) error {
	if kind, ok := arrayKind(key); ok {
		return st.stageArray(ctx, tx, dec, kind)
	}
	if dest := st.headerField(key); dest != nil {
		if err := dec.Decode(dest); err != nil {
			return fmt.Errorf("decode export field %q: %w", key, err)
		}
		return nil
	}
	var skip json.RawMessage
	if err := dec.Decode(&skip); err != nil {
		return fmt.Errorf("skip export field %q: %w", key, err)
	}
	return nil
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
