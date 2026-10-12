// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

// The owned harness receives stdin and acknowledges work through the actual CLI.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWakeAgentHarness_HelperProcess(t *testing.T) {
	if os.Getenv("WAKE_TEST_HELPER") != "1" {
		return
	}
	root := os.Getenv("WAKE_DIR")
	input, err := io.ReadAll(os.Stdin)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "stdin-"+strconv.Itoa(os.Getpid())), input, 0600))
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	encoded, err := json.Marshal(args)
	require.NoError(t, err)
	appendWakeRecord(t, filepath.Join(root, "calls"), string(encoded))
	require.NoError(t, os.WriteFile(filepath.Join(root, "started"), []byte("ready"), 0600))
	waitHarnessGate(t, root)
	if os.Getenv("WAKE_HARNESS_REFRESH") == "1" {
		cmd := exec.Command(os.Getenv("MTIX_BIN"), "inbox", "--agent", "TEST-agent", "--format", "prompt")
		input, err = cmd.Output()
		require.NoError(t, err)
	}
	refillHarnessInbox(t, root)
	ackHarnessInput(t, root, input)
	if os.Getenv("WAKE_HARNESS_FAIL") == "1" {
		os.Exit(7)
	}
	os.Exit(0)
}

func appendWakeRecord(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	require.NoError(t, err)
	_, err = fmt.Fprintln(file, text)
	closeErr := file.Close()
	require.NoError(t, err)
	require.NoError(t, closeErr)
}

func waitHarnessGate(t *testing.T, root string) {
	if os.Getenv("WAKE_HARNESS_GATE") == "1" {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(root, "continue")); err == nil {
				break
			}
			require.True(t, time.Now().Before(deadline), "owned harness gate deadline")
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func ackHarnessInput(t *testing.T, root string, input []byte) {
	for _, match := range regexp.MustCompile(`\[seq ([0-9]+)\]`).FindAllSubmatch(input, -1) {
		seq := string(match[1])
		cmd := exec.Command(os.Getenv("MTIX_BIN"), "inbox", "ack", seq, "--agent", "TEST-agent")
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "harness ACK: %s", output)
		appendWakeRecord(t, filepath.Join(root, "acked"), seq)
	}
}

func refillHarnessInbox(t *testing.T, root string) {
	if os.Getenv("WAKE_HARNESS_REFILL") != "1" {
		return
	}
	calls, err := os.ReadFile(filepath.Join(root, "calls"))
	require.NoError(t, err)
	// A twenty-first run ends this owned stream, making the bound assertion direct.
	if len(bytes.Split(bytes.TrimSpace(calls), []byte("\n"))) > 20 {
		return
	}
	cmd := exec.Command(os.Getenv("MTIX_BIN"), "comment", "TEST-1", "Continuing event", "--to", "TEST-agent")
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "harness event: %s", output)
}
