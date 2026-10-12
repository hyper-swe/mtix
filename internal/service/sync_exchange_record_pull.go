// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Pull ownership binds received completion to a coherent local snapshot.
package service

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"time"
)

type ExchangePull struct {
	service *SyncService
	dir     string
	lock    *os.File
}

type ExchangePullCompletion struct {
	CursorDrained  bool
	SweepCompleted bool
	Encountered    []string
	Reason         string
}

func (s *SyncService) BeginExchangePull(ctx context.Context, dir string) (*ExchangePull, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	lock, err := s.acquireLock(dir, lockExclusive)
	if err != nil {
		if isSyncLockContention(err) {
			return nil, exchangeUnavailable("lock-unavailable", ErrExchangeLockBusy)
		}
		return nil, exchangeUnavailable("lock-unavailable", nil)
	}
	return &ExchangePull{service: s, dir: dir, lock: lock}, nil
}

func (p *ExchangePull) Close() {
	if p.lock != nil {
		p.service.releaseLock(p.lock)
		p.lock = nil
	}
}

func (p *ExchangePull) Complete(ctx context.Context, c ExchangePullCompletion) error {
	if p.lock == nil {
		return exchangeUnavailable("lock-unavailable", nil)
	}
	if c.Reason != "" {
		return p.service.saveExchangeReason(ctx, c.Reason)
	}
	if !c.CursorDrained || !c.SweepCompleted {
		return p.service.saveExchangeReason(ctx, "partial-pull")
	}
	data, b, err := p.service.store.ExportExchangeSnapshot(ctx, "", "", c.Encountered, p.service.MaxImportSize)
	if err != nil {
		return errors.Join(err, p.service.saveExchangeReason(ctx, "snapshot-failed"))
	}
	if b.Unresolved != 0 || b.Pending != 0 || b.SweepAfterID != "" || b.SweepAfterCreatedAt != "" || b.SweepStartedAt != "" {
		return p.service.saveExchangeReason(ctx, "unapplied-event")
	}
	// A successful current Store snapshot always supplies PendingOutbound;
	// retain the nil guard to fail closed if an unknown boundary ever reaches us.
	if b.PendingOutbound == nil || *b.PendingOutbound != 0 {
		return p.service.saveExchangeReason(ctx, "pending-outbound")
	}
	r, err := buildExchangeRecord(data, "hub-pull", "", p.service.clock().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	r.Boundary = &b
	return p.service.publishReceived(ctx, p.dir, r)
}

func (p *ExchangePull) Incomplete(ctx context.Context) error {
	if p.lock == nil {
		return exchangeUnavailable("lock-unavailable", nil)
	}
	return p.service.saveExchangeReason(ctx, "partial-pull")
}

type ExchangeRecordStatus struct {
	Present   bool   `json:"present"`
	WrittenAt string `json:"written_at,omitempty"`
	Source    string `json:"source,omitempty"`
	BoardForm string `json:"board_form,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

func (s *SyncService) ExchangeRecordStatus(ctx context.Context, dir string) (ExchangeRecordStatus, error) {
	var status ExchangeRecordStatus
	if err := ctx.Err(); err != nil {
		return status, err
	}
	r, err := s.LastExchanged(ctx, dir)
	if err == nil {
		status.Present = true
		status.WrittenAt = r.WrittenAt
		status.Source = r.Source
		status.BoardForm = r.BoardForm
		if r.Boundary != nil && r.Boundary.PendingOutbound == nil {
			status.Reason = "outbound-boundary-unknown"
		}
	}
	if err != nil && !errors.Is(err, ErrNoExchangeRecord) {
		return status, err
	}
	if err != nil {
		status.Reason = "unavailable"
		var receiptErr *exchangeRecordError
		if errors.As(err, &receiptErr) {
			status.Reason = receiptErr.category
		}
	}
	var reason string
	readErr := s.store.QueryRow(ctx, `SELECT value FROM meta WHERE key=?`, exchangeReasonKey).Scan(&reason)
	if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
		return status, readErr
	}
	if reason != "" {
		status.Reason = normalizeExchangeReason(reason)
	}
	return status, nil
}
