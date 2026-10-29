// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build windows

package hooks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendFileWindows_HardLinkToAFileOutsideTheProjectIsRefusedAtDelivery(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(t.TempDir(), "rc")
	require.NoError(t, os.WriteFile(outside, []byte("keep\n"), 0o600))
	require.NoError(t, os.Link(outside, filepath.Join(base, "INBOX.md")))
	delivery := Delivery{
		Hook:      Hook{Name: "audit", AppendFile: &AppendFileConfig{Path: "INBOX.md"}},
		Event:     Event{Name: EventNodeCreated, NodeID: "HP-1"},
		EventJSON: []byte(`{}`),
	}
	err := NewAppendFileAdapter(base).Deliver(context.Background(), delivery)
	require.ErrorContains(t, err, "hard link")
	data, readErr := os.ReadFile(outside)
	require.NoError(t, readErr)
	require.Equal(t, "keep\n", string(data))
}

func TestHandleLinkCount_ReturnsTheQueryFailureInsteadOfAssumingOne(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "x")
	require.NoError(t, err)
	defer f.Close()
	cause := errors.New("handle query failed")
	_, err = handleLinkCount(f, func(uintptr, *syscall.ByHandleFileInformation) error { return cause })
	require.ErrorIs(t, err, cause)
	links, err := handleLinkCount(f, func(_ uintptr, data *syscall.ByHandleFileInformation) error {
		data.NumberOfLinks = 3
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, uint64(3), links)
}

func TestVerifyOpened_RefusesWhenTheLinkCountCannotBeRead(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "x")
	require.NoError(t, err)
	require.NoError(t, f.Close()) // a closed handle cannot be queried
	require.Error(t, verifyOpened(f, nil))
}
