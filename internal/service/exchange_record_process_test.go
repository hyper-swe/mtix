// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

// Owned subprocess barriers prove exclusion and pre-publication crash behavior.
package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

type exchangeGateWriter struct{ *os.File }

func (w exchangeGateWriter) Write(raw []byte) (int, error) {
	n, err := w.File.Write(raw[:1])
	if err != nil {
		return n, err
	}
	if _, err := fmt.Fprintln(os.Stdout, "receipt-ready"); err != nil {
		return n, err
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return n, err
	}
	rest, err := w.File.Write(raw[1:])
	return n + rest, err
}

func TestExchangeRecord_ProcessHelper(t *testing.T) {
	if os.Getenv("MTIX_EXCHANGE_PROCESS") != "1" {
		return
	}
	dir := os.Getenv("MTIX_EXCHANGE_DIR")
	st, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	s := NewSyncService(st, slog.Default(), func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) })
	s.wrapExchangeFile = func(f *os.File) io.WriteCloser { return exchangeGateWriter{File: f} }
	require.NoError(t, s.AutoImport(t.Context(), dir))
}

type exchangeChild struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Reader
	waited bool
}

func startExchangeChild(t *testing.T, dir string) *exchangeChild {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExchangeRecord_ProcessHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "MTIX_EXCHANGE_PROCESS=1", "MTIX_EXCHANGE_DIR="+dir)
	input, err := cmd.StdinPipe()
	require.NoError(t, err)
	output, err := cmd.StdoutPipe()
	require.NoError(t, err)
	child := &exchangeChild{cmd: cmd, input: input, output: bufio.NewReader(output)}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		closeErr := input.Close()
		require.True(t, closeErr == nil || errors.Is(closeErr, os.ErrClosed))
		if !child.waited {
			err := cmd.Process.Kill()
			require.True(t, err == nil || errors.Is(err, os.ErrProcessDone))
			waitErr := cmd.Wait()
			var exitErr *exec.ExitError
			require.True(t, waitErr == nil || errors.As(waitErr, &exitErr))
			child.waited = true
		}
	})
	line, err := child.output.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "receipt-ready\n", line)
	return child
}

func TestExchangeRecord_CrashMidWrite_OldRecordIntact(t *testing.T) {
	s, dir := exchangeFixture(t)
	exchangePublishReference(t, s, dir)
	before, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(exchangeCurrentJSON), 0o644))
	child := startExchangeChild(t, dir)
	require.NoError(t, child.cmd.Process.Kill())
	require.Error(t, child.cmd.Wait())
	child.waited = true
	after, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = s.LastExchanged(t.Context(), dir)
	require.ErrorIs(t, err, ErrNoExchangeRecord)
	temps, err := filepath.Glob(filepath.Join(dir, "data", ".exchange-record-*"))
	require.NoError(t, err)
	require.Len(t, temps, 1)
}

func TestExchangeRecord_ConcurrentImporters_NoInterleave(t *testing.T) {
	s, dir := exchangeFixture(t)
	exchangePublishReference(t, s, dir)
	before, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tasks.json"), []byte(exchangeCurrentJSON), 0o644))
	child := startExchangeChild(t, dir)
	require.NoError(t, s.AutoImport(t.Context(), dir), "contended importer skips without interleaving")
	after, err := os.ReadFile(exchangePath(dir))
	require.NoError(t, err)
	require.Equal(t, before, after)
	_, err = fmt.Fprintln(child.input, "resume")
	require.NoError(t, err)
	require.NoError(t, child.cmd.Wait())
	child.waited = true
	loaded, err := s.LastExchanged(t.Context(), dir)
	require.NoError(t, err)
	require.Equal(t, "2026-10-02T00:00:00Z", loaded.WrittenAt)
}
