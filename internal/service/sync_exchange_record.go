// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Received provenance is independent of future incoming-lineage decisions.
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

var ErrNoExchangeRecord = errors.New("no exchange record")

const exchangeReasonKey = "meta.sync.exchange_record.reason"

type ExchangeTask struct {
	Kind     string            `json:"kind"`
	Identity string            `json:"identity"`
	ID       string            `json:"id"`
	Hash     string            `json:"h"`
	Fields   map[string]string `json:"fields"`
}

type ExchangeRecord struct {
	Format          int                      `json:"format"`
	WrittenAt       string                   `json:"written_at"`
	Source          string                   `json:"source"`
	BoardForm       string                   `json:"board_form"`
	BoardHash       string                   `json:"board_hash"`
	BoardFileHash   string                   `json:"board_file_hash,omitempty"`
	ParentBoardHash string                   `json:"parent_board_hash,omitempty"`
	NodeCount       int                      `json:"node_count"`
	Tasks           []ExchangeTask           `json:"tasks"`
	Boundary        *sqlite.ExchangeBoundary `json:"boundary,omitempty"`
}

func exchangePath(dir string) string { return filepath.Join(dir, "data", "exchange-record.json") }

type exchangeRecordError struct{ category string }

func (e *exchangeRecordError) Error() string { return "exchange record: " + e.category }
func (e *exchangeRecordError) Unwrap() error { return ErrNoExchangeRecord }

func exchangeUnavailable(category string, cause error) error {
	return errors.Join(&exchangeRecordError{category: category}, cause)
}

// LastExchanged only loads received provenance, without judging incoming lineage.
func (s *SyncService) LastExchanged(ctx context.Context, dir string) (*ExchangeRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, exchangeUnavailable("context", err)
	}
	if !exchangePlatformSupported() {
		return nil, exchangeUnavailable("unsupported", nil)
	}
	if s.MaxImportSize <= 0 {
		return nil, exchangeUnavailable("invalid-cap", nil)
	}
	if _, err := os.Lstat(exchangePath(dir)); err != nil {
		return nil, exchangeUnavailable("missing", nil)
	}
	lock, err := s.openExchangeReadLock(dir)
	if err != nil {
		return nil, exchangeUnavailable("lock-unavailable", nil)
	}
	defer s.releaseLock(lock)
	return s.readExchangeRecord(ctx, dir)
}

func (s *SyncService) readExchangeRecord(ctx context.Context, dir string) (*ExchangeRecord, error) {
	info, err := os.Lstat(exchangePath(dir))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, exchangeUnavailable("unsafe-file", nil)
	}
	temps, err := filepath.Glob(filepath.Join(dir, "data", ".exchange-record-*"))
	if err != nil || len(temps) > 0 {
		return nil, exchangeUnavailable("temporary-file", nil)
	}
	if info.Size() > s.MaxImportSize {
		return nil, exchangeUnavailable("oversize", nil)
	}
	raw, err := readBoundedExchange(exchangePath(dir), s.MaxImportSize)
	if err != nil {
		return nil, exchangeUnavailable("read", nil)
	}
	var r ExchangeRecord
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return nil, exchangeUnavailable("invalid-json", nil)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, exchangeUnavailable("trailing-json", nil)
	}
	if err := validateExchangeRecord(&r); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, exchangeUnavailable("context", err)
	}
	return &r, nil
}

func readBoundedExchange(path string, capBytes int64) (raw []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	// Avoid overflow of cap+1 at the maximum int64 boundary.
	raw, err = io.ReadAll(io.LimitReader(f, capBytes))
	if err != nil {
		return nil, err
	}
	var extra [1]byte
	n, readErr := f.Read(extra[:])
	if n > 0 {
		return nil, exchangeUnavailable("oversize", nil)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	return raw, nil
}

func validateExchangeRecord(r *ExchangeRecord) error {
	if r.Format != 1 || (r.Source != "git-import" && r.Source != "hub-pull") ||
		(r.BoardForm != "current" && r.BoardForm != "schema-1.0.0") {
		return exchangeUnavailable("format", nil)
	}
	stamp, err := time.Parse(time.RFC3339, r.WrittenAt)
	if err != nil || stamp.Location() != time.UTC {
		return exchangeUnavailable("time", nil)
	}
	if !exchangeDigest(r.BoardHash) || !optionalExchangeDigest(r.ParentBoardHash) ||
		!optionalExchangeDigest(r.BoardFileHash) {
		return exchangeUnavailable("hash", nil)
	}
	if err := validateExchangeSource(r); err != nil {
		return err
	}
	if r.NodeCount != len(r.Tasks) || r.Tasks == nil {
		return exchangeUnavailable("count", nil)
	}
	if err := validateExchangeBoundary(r); err != nil {
		return err
	}
	return validateExchangeTasks(r.Tasks)
}

func validateExchangeSource(r *ExchangeRecord) error {
	if (r.Source == "git-import" && (r.BoardFileHash == "" || r.Boundary != nil)) ||
		(r.Source == "hub-pull" && (r.BoardFileHash != "" || r.Boundary == nil)) {
		return exchangeUnavailable("source", nil)
	}
	return nil
}

func validateExchangeBoundary(r *ExchangeRecord) error {
	if r.Boundary == nil {
		return nil
	}
	b := r.Boundary
	if b.LocalSequence < 0 || b.PullClock < 0 || b.Pending != 0 || b.Unresolved != 0 {
		return exchangeUnavailable("boundary", nil)
	}
	if b.SweepAfterID != "" || b.SweepAfterCreatedAt != "" || b.SweepStartedAt != "" {
		return exchangeUnavailable("boundary", nil)
	}
	if _, err := time.Parse(time.RFC3339Nano, b.LastSweepAt); err != nil {
		return exchangeUnavailable("boundary", nil)
	}
	return nil
}

// normalizeExchangeReason exposes only fixed local diagnostic categories.
func normalizeExchangeReason(reason string) string {
	switch reason {
	case "", "unknown", "unavailable", "context", "unsupported", "missing", "invalid-cap", "lock-unavailable",
		"unsafe-file", "temporary-file", "oversize", "read", "invalid-json", "trailing-json", "format", "time", "hash",
		"source", "count", "boundary", "task-identity", "task-fields", "historical-form", "board-hash", "task-json",
		"unsupported-schema", "encode", "publish-failed", "durability-uncertain", "partial-pull", "snapshot-failed",
		"unapplied-event", "tracking-limit", "unknown-event":
		return reason
	default:
		return "unknown"
	}
}
