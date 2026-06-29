// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-32: sync write commands must honor the NFR-2.8 CLI exit-code contract.
// wrapSyncErr stringified the error (DSN redaction), which broke the
// errors.Is chain, so a disk-full during a sync write (e.g. backfill) exited
// 1 instead of the contract code 3. These tests pin that the disk-full and
// corrupted sentinels survive wrapSyncErr so exitCodeForError maps them
// correctly, while a generic error stays generic and DSNs stay redacted.
package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWrapSyncErr_PreservesDiskFullExitCode(t *testing.T) {
	err := wrapSyncErr(io.Discard, "backfill", fmt.Errorf("write refused: %w", model.ErrDiskFull))
	require.Error(t, err)
	assert.Equal(t, exitCodeDiskFull, exitCodeForError(err),
		"disk-full during a sync write must exit 3 (NFR-2.8), not generic 1")
}

func TestWrapSyncErr_PreservesCorruptedExitCode(t *testing.T) {
	err := wrapSyncErr(io.Discard, "backfill", fmt.Errorf("integrity gate: %w", model.ErrCorrupted))
	require.Error(t, err)
	assert.Equal(t, exitCodeCorrupted, exitCodeForError(err))
}

func TestWrapSyncErr_GenericStaysGeneric(t *testing.T) {
	err := wrapSyncErr(io.Discard, "push", errors.New("some transport error"))
	require.Error(t, err)
	assert.Equal(t, exitCodeGeneric, exitCodeForError(err))
}

// A disk-full error whose text embeds a DSN must still exit 3 AND not leak the
// DSN in the message — sentinel preservation must not bypass redaction.
func TestWrapSyncErr_DiskFullStillRedactsDSN(t *testing.T) {
	dsnErr := fmt.Errorf(
		"write to postgres://user:secretpw@db.example.com:5432/mtix refused: %w",
		model.ErrDiskFull)
	err := wrapSyncErr(io.Discard, "push", dsnErr)
	require.Error(t, err)
	assert.Equal(t, exitCodeDiskFull, exitCodeForError(err))
	assert.NotContains(t, err.Error(), "secretpw", "DSN credentials must stay redacted")
	assert.False(t, strings.Contains(err.Error(), "secretpw"))
}
