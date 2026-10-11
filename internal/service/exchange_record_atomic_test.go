// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Publication errors distinguish unchanged old bytes from complete new bytes.
package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type exchangeFaultWriter struct {
	*os.File
	kind string
}

func (w exchangeFaultWriter) Write(raw []byte) (int, error) {
	switch w.kind {
	case "short":
		return w.File.Write(raw[:1])
	case "write":
		return 0, io.ErrUnexpectedEOF
	case "sync":
		n, err := w.File.Write(raw)
		return n, errors.Join(err, w.File.Close())
	}
	return w.File.Write(raw)
}
func (w exchangeFaultWriter) Close() error {
	err := w.File.Close()
	if w.kind == "close" {
		return errors.Join(err, io.ErrUnexpectedEOF)
	}
	return err
}

func TestExchangeRecord_PublicationErrors_PreservePriorBytes(t *testing.T) {
	for _, kind := range []string{"write", "short", "close", "sync", "rename"} {
		t.Run(kind, func(t *testing.T) {
			s, dir := exchangeFixture(t)
			old := exchangePublishReference(t, s, dir)
			before, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			s.wrapExchangeFile = func(f *os.File) io.WriteCloser {
				if kind == "rename" {
					return f
				}
				return exchangeFaultWriter{File: f, kind: kind}
			}
			if kind == "rename" {
				require.NoError(t, os.Rename(exchangePath(dir), exchangePath(dir)+".saved"))
				require.NoError(t, os.Mkdir(exchangePath(dir), 0o700))
			}
			r := *old
			r.WrittenAt = "2026-10-02T00:00:00Z"
			lock, err := s.acquireLock(dir, lockExclusive)
			require.NoError(t, err)
			err = s.publishReceived(t.Context(), dir, &r)
			s.releaseLock(lock)
			require.Error(t, err)
			if kind == "rename" {
				require.NoError(t, os.Remove(exchangePath(dir)))
				require.NoError(t, os.Rename(exchangePath(dir)+".saved", exchangePath(dir)))
			}
			after, err := os.ReadFile(exchangePath(dir))
			require.NoError(t, err)
			require.Equal(t, before, after)
			temps, err := filepath.Glob(filepath.Join(dir, "data", ".exchange-record-*"))
			require.NoError(t, err)
			require.Empty(t, temps)
		})
	}
}

func TestExchangeRecord_DirectorySyncFailure_NewRecordRemainsLoadable(t *testing.T) {
	s, dir := exchangeFixture(t)
	old := exchangePublishReference(t, s, dir)
	s.directorySync = func(string) error { return io.ErrUnexpectedEOF }
	next := *old
	next.WrittenAt = "2026-10-02T00:00:00Z"
	lock, err := s.acquireLock(dir, lockExclusive)
	require.NoError(t, err)
	err = s.publishReceived(t.Context(), dir, &next)
	s.releaseLock(lock)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	got, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, next.WrittenAt, got.WrittenAt)
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, "durability-uncertain", status.Reason)
}

func TestExchangeRecord_ReasonClearFailure_NewRecordRemainsLoadable(t *testing.T) {
	s, dir := exchangeFixture(t)
	old := exchangePublishReference(t, s, dir)
	require.NoError(t, s.saveExchangeReason(t.Context(), "partial-pull"))
	s.directorySync = func(string) error { return s.store.Close() }
	next := *old
	next.WrittenAt = "2026-10-02T00:00:00Z"
	lock, err := s.acquireLock(dir, lockExclusive)
	require.NoError(t, err)
	err = s.publishReceived(t.Context(), dir, &next)
	s.releaseLock(lock)
	require.Error(t, err)
	got, err := s.LastExchanged(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, next.WrittenAt, got.WrittenAt)
}

func TestExchangeRecord_FailedReceiptAndHashMatch_NoInventedRepair(t *testing.T) {
	s, dir := exchangeFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(exchangeCurrentJSON), 0o644))
	s.wrapExchangeFile = func(f *os.File) io.WriteCloser { return exchangeFaultWriter{File: f, kind: "write"} }
	require.Error(t, s.AutoImport(t.Context(), dir))
	var count int
	require.NoError(t, s.store.QueryRow(t.Context(), `SELECT COUNT(*) FROM nodes`).Scan(&count))
	require.Equal(t, 1, count)
	s.wrapExchangeFile = nil
	require.NoError(t, s.AutoImport(t.Context(), dir))
	_, err := os.Stat(exchangePath(dir))
	require.ErrorIs(t, err, os.ErrNotExist)
	status, err := s.ExchangeRecordStatus(t.Context(), dir)
	require.NoError(t, err)
	require.False(t, status.Present)
	require.Equal(t, "publish-failed", status.Reason)
}

type exchangeCancelWriter struct {
	*os.File
	cancel context.CancelFunc
}

func (w exchangeCancelWriter) Write(raw []byte) (int, error) {
	n, err := w.File.Write(raw)
	w.cancel()
	return n, err
}

func TestExchangeRecord_CancelAfterOwnedWrite_PreservesPriorRecord(t *testing.T) {
	s, dir := exchangeFixture(t)
	prior := exchangePublishReference(t, s, dir)
	before, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(exchangeCurrentJSON), 0o644))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s.wrapExchangeFile = func(f *os.File) io.WriteCloser { return exchangeCancelWriter{File: f, cancel: cancel} }
	require.ErrorIs(t, s.AutoImport(ctx, dir), context.Canceled)
	after, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.Equal(t, before, after)
	r, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, prior, r)
	temps, err := filepath.Glob(filepath.Join(dir, "data", ".exchange-record-*"))
	require.NoError(t, err)
	require.Empty(t, temps)
}
