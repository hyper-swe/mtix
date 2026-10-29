// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Count output propagates writer and storage failures instead of success.
package main

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type countFailWriter struct{ err error }

func (w countFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestInboxCountCLI_OutputFailure_PreservesError(t *testing.T) {
	for _, tt := range []struct {
		name string
		json bool
	}{{"text", false}, {"JSON", true}} {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			app.jsonOutput = tt.json
			failure := errors.New("count output unavailable")
			cmd := newInboxCmd()
			cmd.SetArgs([]string{"--count", "--agent", "never"})
			cmd.SetOut(countFailWriter{failure})
			require.ErrorIs(t, cmd.Execute(), failure)
		})
	}
}

func TestInboxCountCLI_MissingOrClosedStore_ReturnsError(t *testing.T) {
	for _, closed := range []bool{false, true} {
		initTestApp(t)
		cmd := newInboxCmd()
		cmd.SetArgs([]string{"--count", "--agent", "w"})
		require.NoError(t, app.store.Close())
		if !closed {
			app.store = nil
		}
		err := cmd.Execute()
		require.Error(t, err)
		if !closed {
			require.ErrorContains(t, err, "not in an mtix project")
		} else {
			require.ErrorContains(t, err, "inbox count")
		}
	}
}
