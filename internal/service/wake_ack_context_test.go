// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

func TestWakeAckPublication_OperatorErrorsNameTheFailedOperation(t *testing.T) {
	for _, tc := range []struct{ name, context string }{
		{"state", "resolve acknowledgement operator state"},
		{"host", "read acknowledgement host identity"},
		{"accounting", "read acknowledgement wake accounting"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, key, source := wakePublisherFixture(t)
			cause := model.ErrOperatorStateUnreadable
			p := &wakeAckPublisher{store: wakePublisherBackend{source: source, mark: 2},
				state: func() (*oplocal.State, string, error) { return state, key.UID, nil }}
			if tc.name == "state" {
				cause = errors.New("operator identity unavailable")
				p.state = func() (*oplocal.State, string, error) { return nil, "", cause }
			} else {
				dir, err := state.Path()
				require.NoError(t, err)
				path := filepath.Join(dir, "host-id")
				if tc.name == "accounting" {
					files, err := filepath.Glob(filepath.Join(dir, "wake-budget-*.json"))
					require.NoError(t, err)
					require.Len(t, files, 1)
					path = files[0]
				}
				require.NoError(t, os.WriteFile(path, []byte("invalid protected data"), 0o600))
			}
			err := p.Observe(t.Context(), key.AgentID, 1, 2)
			require.ErrorIs(t, err, cause)
			require.ErrorContains(t, err, tc.context)
		})
	}
}
