// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPendingExports_ChildFailure_ReportsBothStreamsAndExit(t *testing.T) {
	for _, mode := range []string{"failure", "large-failure", "exit"} {
		t.Run(mode, func(t *testing.T) {
			cmd := diagnosticProbe(t, "report-"+mode)
			out, err := cmd.CombinedOutput()
			require.Error(t, err, "the probe parent must fail on its child exit")
			text := string(out)
			require.Contains(t, text, "child stdout sentinel")
			require.Contains(t, text, "child stderr sentinel")
			require.Contains(t, text, "exit status 23")
			if mode == "large-failure" {
				require.Less(t, len(out), 40*1024, "retained output must be bounded")
				require.NotContains(t, text, "beyond retained limit")
			}
		})
	}
}

func TestPendingExports_ChildNoise_DrainsAndPreservesAllSignals(t *testing.T) {
	out, err := diagnosticProbe(t, "report-protocol").CombinedOutput()
	require.NoError(t, err, string(out))
}

func diagnosticProbe(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestPendingExports_ChildDiagnosticProbe$", "--", mode)
	cmd.Env = coalescingWriterEnv()
	return cmd
}

func diagnosticChild(t *testing.T, mode string) *coalescingWriter {
	t.Helper()
	child := &coalescingWriter{lines: make(chan string, 8), done: make(chan struct{})}
	child.cmd = diagnosticProbe(t, mode)
	child.cancel = func() {} // diagnosticProbe owns cancellation through t.Cleanup.
	child.cmd.Stderr = &child.stderr
	var err error
	child.stdout, err = child.cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, child.cmd.Start())
	t.Cleanup(func() { child.close(t) })
	go child.readAndWait(child.stdout)
	return child
}

// Re-exec probes isolate the expected fatal parent assertion from the real test.
func TestPendingExports_ChildDiagnosticProbe(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "--" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	if strings.HasPrefix(mode, "report-") {
		child := diagnosticChild(t, strings.TrimPrefix(mode, "report-"))
		if mode == "report-protocol" {
			for _, signal := range diagnosticSignals() {
				child.awaitSignal(t, signal)
			}
			child.awaitExit(t)
			return
		}
		if mode == "report-exit" {
			child.awaitSignal(t, "ready")
			child.awaitExit(t)
			return
		}
		child.awaitSignal(t, "marked 32")
		return
	}
	_, err := fmt.Fprintln(os.Stdout, "child stdout sentinel")
	require.NoError(t, err)
	_, err = fmt.Fprintln(os.Stderr, "child stderr sentinel")
	require.NoError(t, err)
	if mode == "large-failure" || mode == "protocol" {
		for range 600 {
			_, err = fmt.Fprintln(os.Stdout, strings.Repeat("diagnostic", 10))
			require.NoError(t, err)
		}
		_, err = fmt.Fprintln(os.Stdout, "beyond retained limit")
		require.NoError(t, err)
	}
	if mode == "protocol" {
		for _, signal := range diagnosticSignals() {
			_, err = fmt.Fprintln(os.Stdout, signal)
			require.NoError(t, err)
		}
		return
	}
	if mode == "exit" {
		_, err = fmt.Fprintln(os.Stdout, "ready")
		require.NoError(t, err)
	}
	os.Exit(23) // Only this owned re-exec probe exits, with no store or resource open.
}

func diagnosticSignals() []string {
	return []string{"ready", "snapshot-held", "committed 2", "writer-held", "force-result ok", "marked 32"}
}
