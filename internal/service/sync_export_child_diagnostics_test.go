// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPendingExports_ChildFailure_ReportsBothStreamsAndExit(t *testing.T) {
	for _, tc := range []struct {
		mode                              string
		stdoutTail, stderrTail, truncated bool
		maxBytes                          int
	}{
		{"failure", false, false, false, 40 * 1024},
		{"large-failure", true, false, true, 40 * 1024},
		{"exit", false, false, false, 40 * 1024},
		{"long-stdout", true, false, true, 40 * 1024},
		{"long-stderr", false, true, true, 40 * 1024},
		{"both-long", true, true, true, 80 * 1024},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			out, err := diagnosticProbe(t, "report-"+tc.mode).CombinedOutput()
			require.Error(t, err, "the probe parent must fail on its child exit")
			text := string(out)
			require.Contains(t, text, "child stdout sentinel")
			require.Contains(t, text, "child stderr sentinel")
			require.Contains(t, text, "exit status 23")
			require.Less(t, len(out), tc.maxBytes, "both retained streams must stay bounded")
			if tc.truncated {
				require.Contains(t, text, "[truncated ")
			}
			if tc.stdoutTail {
				require.Contains(t, text, "child stdout tail sentinel")
			}
			if tc.stderrTail {
				require.Contains(t, text, "child stderr tail sentinel")
			}
		})
	}
}

func TestPendingExports_ChildNoise_DrainsAndPreservesAllSignals(t *testing.T) {
	for _, mode := range []string{"protocol", "protocol-no-newline", "protocol-long-noise"} {
		t.Run(mode, func(t *testing.T) {
			out, err := diagnosticProbe(t, "report-"+mode).CombinedOutput()
			require.NoError(t, err, string(out))
		})
	}
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
		runDiagnosticParent(t, strings.TrimPrefix(mode, "report-"))
		return
	}
	emitDiagnosticChild(t, mode)
}

func runDiagnosticParent(t *testing.T, mode string) {
	t.Helper()
	child := diagnosticChild(t, mode)
	if strings.HasPrefix(mode, "protocol") {
		var got []string
		for signal := range child.lines {
			got = append(got, signal)
		}
		child.awaitExit(t)
		require.Equal(t, diagnosticSignals(), got, "only whole protocol lines may queue")
		return
	}
	if mode == "exit" {
		child.awaitSignal(t, "ready")
		child.awaitExit(t)
		return
	}
	child.awaitSignal(t, "marked 32")
}

func diagnosticLine(t *testing.T, w io.Writer, text string) {
	t.Helper()
	_, err := fmt.Fprintln(w, text)
	require.NoError(t, err)
}

func emitDiagnosticChild(t *testing.T, mode string) {
	t.Helper()
	diagnosticLine(t, os.Stdout, "child stdout sentinel")
	diagnosticLine(t, os.Stderr, "child stderr sentinel")
	if mode == "large-failure" || strings.HasPrefix(mode, "protocol") {
		for range 600 {
			diagnosticLine(t, os.Stdout, strings.Repeat("diagnostic", 10))
		}
	}
	if mode == "long-stdout" || mode == "both-long" || mode == "protocol-long-noise" {
		diagnosticLine(t, os.Stdout, "force-result "+strings.Repeat("x", 300*1024))
	}
	if mode == "large-failure" || mode == "long-stdout" || mode == "both-long" {
		diagnosticLine(t, os.Stdout, "child stdout tail sentinel")
	}
	if mode == "long-stderr" || mode == "both-long" {
		diagnosticLine(t, os.Stderr, strings.Repeat("x", 300*1024))
		diagnosticLine(t, os.Stderr, "child stderr tail sentinel")
	}
	if strings.HasPrefix(mode, "protocol") {
		emitDiagnosticSignals(t, mode)
		return
	}
	if mode == "exit" {
		diagnosticLine(t, os.Stdout, "ready")
	}
	os.Exit(23) // Only this owned re-exec probe exits, with no store open.
}

func emitDiagnosticSignals(t *testing.T, mode string) {
	t.Helper()
	signals := diagnosticSignals()
	for i, signal := range signals {
		if mode == "protocol-no-newline" {
			ending := "\r\n"
			if i == len(signals)-1 {
				ending = ""
			}
			_, err := fmt.Fprint(os.Stdout, signal+ending)
			require.NoError(t, err)
		} else {
			diagnosticLine(t, os.Stdout, signal)
		}
	}
	if mode == "protocol-no-newline" {
		os.Exit(0) // Suppress testing's PASS line so the final signal really ends at EOF.
	}
}

func diagnosticSignals() []string {
	return []string{"ready", "snapshot-held", "committed 2", "writer-held", "force-result ok", "marked 32"}
}
