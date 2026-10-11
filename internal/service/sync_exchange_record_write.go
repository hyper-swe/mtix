// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Publication retains received bytes independently of export bookkeeping.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func (s *SyncService) publishReceived(ctx context.Context, dir string, r *ExchangeRecord) error {
	if !exchangePlatformSupported() {
		return exchangeUnavailable("unsupported", nil)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.MaxImportSize <= 0 {
		return exchangeUnavailable("invalid-cap", nil)
	}
	// The previous association is local provenance, never proof of ancestry.
	if old, err := s.readExchangeRecord(ctx, dir); err == nil && old.BoardHash != r.BoardHash {
		r.ParentBoardHash = old.BoardHash
	}
	if err := validateExchangeRecord(r); err != nil {
		return err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return exchangeUnavailable("encode", nil)
	}
	if int64(len(raw)) > s.MaxImportSize {
		return exchangeUnavailable("oversize", nil)
	}
	err = s.writeExchangeRecord(ctx, exchangePath(dir), raw)
	if err != nil {
		category := "publish-failed"
		var receiptErr *exchangeRecordError
		if errors.As(err, &receiptErr) {
			category = receiptErr.category
		}
		return errors.Join(err, s.saveExchangeReason(ctx, category))
	}
	return s.saveExchangeReason(ctx, "")
}

func (s *SyncService) writeExchangeRecord(ctx context.Context, path string, raw []byte) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".exchange-record-*")
	if err != nil {
		return err
	}
	published := false
	defer func() {
		err = errors.Join(err, closeExchangeFile(f))
		if !published {
			err = errors.Join(err, os.Remove(f.Name()))
		}
	}()
	var writer io.WriteCloser = f
	if s.wrapExchangeFile != nil {
		writer = s.wrapExchangeFile(f)
	}
	if n, writeErr := writer.Write(raw); writeErr != nil || n != len(raw) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return writeErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	closeErr := writer.Close()
	if closeErr != nil {
		return closeErr
	}
	if err := publishExchangeFile(f.Name(), path); err != nil {
		return err
	}
	published = true
	if err := s.syncDirectory(filepath.Dir(path)); err != nil {
		return exchangeUnavailable("durability-uncertain", err)
	}
	return nil
}

func (s *SyncService) saveExchangeReason(ctx context.Context, reason string) error {
	return s.store.SaveExchangeRecordReason(ctx, normalizeExchangeReason(reason))
}

func (s *SyncService) recordReceivedImport(ctx context.Context, dir string, incoming *sqlite.ExportData, rawHash string) error {
	r, err := buildExchangeRecord(incoming, "git-import", rawHash, s.clock().UTC().Format(time.RFC3339))
	if err != nil {
		var receiptErr *exchangeRecordError
		if errors.As(err, &receiptErr) && receiptErr.category == "unsupported-schema" {
			// Compatible imports remain successful without inventing a receipt form.
			return s.saveExchangeReason(ctx, "unsupported-schema")
		}
		return err
	}
	return s.publishReceived(ctx, dir, r)
}

func closeExchangeFile(f *os.File) error {
	err := f.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}
