// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package oplocal

import (
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestState_OperationsValidateInput(t *testing.T) {
	for _, operation := range []string{"read", "write", "host", "inspect"} {
		t.Run(operation, func(t *testing.T) {
			s := fixture(t)
			require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 1}))
			require.NoError(t, s.Ensure())
			require.NoError(t, os.Mkdir(filepath.Join(s.env.Root, "home/test/.mtix"), 0700))
			var err error
			switch operation {
			case "read":
				var got any
				_, err = s.ReadJSON("hooks", &got)
			case "write":
				err = s.WriteJSON("hooks", nil)
			case "host":
				_, err = s.HostID()
			case "inspect":
				_, err = s.Inspect()
			}
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		})
	}
}
func TestReadJSON_ValidateRecordInput(t *testing.T) {
	for _, body := range []string{`{"host_id":"0123456789abcdef0123456789abcdef","data":{}}`, `{"host_id":"0123456789abcdef0123456789abcdef","data":{}} {}`} {
		t.Run(body, func(t *testing.T) {
			s := fixture(t)
			require.NoError(t, s.Ensure())
			require.NoError(t, os.WriteFile(filepath.Join(s.pathForTest(t), "hooks.json"), []byte(body), 0600))
			var got any
			_, err := s.ReadJSON("hooks", &got)
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		})
	}
}
func TestWriteJSON_ValidateInput(t *testing.T) {
	for _, name := range []string{"", "../hooks", "/hooks", "host-id", "hooks.json"} {
		t.Run(name, func(t *testing.T) {
			s := fixture(t)
			require.ErrorIs(t, s.WriteJSON(name, nil), model.ErrOperatorStateUnreadable)
		})
	}
	s := fixture(t)
	require.ErrorIs(t, s.WriteJSON("hooks", make(chan int)), model.ErrOperatorStateUnreadable)
}
func TestHostID_ValidateInput(t *testing.T) {
	for _, body := range []string{"", "invalid", "ABCDEF0123456789ABCDEF0123456789"} {
		t.Run(body, func(t *testing.T) {
			s := fixture(t)
			require.NoError(t, s.Ensure())
			require.NoError(t, os.WriteFile(filepath.Join(s.pathForTest(t), "host-id"), []byte(body), 0600))
			_, err := s.HostID()
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		})
	}
}
func TestReadJSON_ValidateFileInput(t *testing.T) {
	for _, kind := range []string{"directory", "link", "large"} {
		t.Run(kind, func(t *testing.T) {
			s := fixture(t)
			require.NoError(t, s.Ensure())
			file := filepath.Join(s.pathForTest(t), "hooks.json")
			switch kind {
			case "directory":
				require.NoError(t, os.Mkdir(file, 0700))
			case "link":
				require.NoError(t, os.Symlink("host-id", file))
			case "large":
				require.NoError(t, os.WriteFile(file, make([]byte, 1024*1024+1), 0600))
			}
			var got any
			_, err := s.ReadJSON("hooks", &got)
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		})
	}
}
func TestInspect_ValidInput(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 1}))
	status, err := s.Inspect()
	require.NoError(t, err)
	require.Equal(t, "ready", status.Status)
	require.NoError(t, os.WriteFile(filepath.Join(s.pathForTest(t), "hooks.json"), []byte(`{"host_id":"00000000000000000000000000000000","data":{}}`), 0600))
	status, err = s.Inspect()
	require.NoError(t, err)
	require.Len(t, status.Reports, 1)
}

func TestFileMetadata_ValidateInput(t *testing.T) {
	for _, tc := range []struct {
		uid, mode uint32
		directory bool
	}{
		{uint32(os.Geteuid() + 1), 0100600, false},
		{uint32(os.Geteuid()), 0100644, false},
		{uint32(os.Geteuid()), 0040700, false},
		{uint32(os.Geteuid()), 0100600, true},
	} {
		require.ErrorIs(t, validateMetadata(tc.uid, tc.mode, tc.directory), model.ErrOperatorStateUnreadable)
	}
}
func TestState_ReadFailures(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	configured, err := s.Path()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(s.env.Root, "home", "test", ".config", "mtix"), configured)
	require.NoError(t, s.WriteJSON("hooks", map[string]int{"value": 1}))
	var wrongType int
	_, err = s.ReadJSON("hooks", &wrongType)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.NoError(t, os.Chmod(s.pathForTest(t), 0200))
	var got any
	_, err = s.ReadJSON("hooks", &got)
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.NoError(t, os.Chmod(s.pathForTest(t), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(s.pathForTest(t), "host-id"), []byte("invalid"), 0600))
	_, err = s.Inspect()
	require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}
func TestState_MetadataUnavailable(t *testing.T) {
	s := fixture(t)
	require.NoError(t, s.Ensure())
	dir, err := s.directory(false)
	require.NoError(t, err)
	require.NoError(t, dir.Close())
	require.ErrorIs(t, verifyFile(dir, true), model.ErrOperatorStateUnreadable)
	require.ErrorIs(t, closeFile(dir), model.ErrOperatorStateUnreadable)
}
