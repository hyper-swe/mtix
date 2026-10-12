// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

// Identity IO failures preserve their causes and close owned descriptors.

import (
	"errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"io"
	"strings"
	"testing"
)

type identityWriter struct {
	writeErr, syncErr, closeErr error
	written                     string
	synced, closed              bool
}

func (w *identityWriter) Write(raw []byte) (int, error) {
	w.written += string(raw)
	return len(raw), w.writeErr
}
func (w *identityWriter) Sync() error  { w.synced = true; return w.syncErr }
func (w *identityWriter) Close() error { w.closed = true; return w.closeErr }

type identityReader struct {
	io.Reader
	closeErr error
	closed   bool
}

func (r *identityReader) Close() error { r.closed = true; return r.closeErr }

type failingIdentityReader struct{ err error }

func (r failingIdentityReader) Read([]byte) (int, error) { return 0, r.err }

func TestCreateBoardID_MintFailure_PreservesCause(t *testing.T) {
	failure := errors.New("identity mint refused")
	_, err := createBoardID(t.TempDir(), func() (uuid.UUID, error) { return uuid.Nil, failure })
	assert.ErrorIs(t, err, failure)
}

func TestWriteBoardIdentity_IOFailures_CloseDescriptor(t *testing.T) {
	failure := errors.New("identity IO refused")
	for _, writer := range []*identityWriter{{}, {writeErr: failure}, {syncErr: failure}, {closeErr: failure}} {
		err := writeBoardIdentity(writer, "identity")
		if writer.writeErr != nil || writer.syncErr != nil || writer.closeErr != nil {
			assert.ErrorIs(t, err, failure)
		} else {
			require.NoError(t, err)
		}
		assert.True(t, writer.synced)
		assert.True(t, writer.closed)
		assert.Equal(t, "identity\n", writer.written)
	}
}

func TestReadBoardIdentity_IOFailures_CloseDescriptor(t *testing.T) {
	failure := errors.New("identity IO refused")
	for _, reader := range []*identityReader{{Reader: failingIdentityReader{failure}}, {Reader: strings.NewReader("identity"), closeErr: failure}} {
		_, err := readBoardIdentity(reader)
		assert.ErrorIs(t, err, failure)
		assert.True(t, reader.closed)
	}
}
