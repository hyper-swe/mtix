// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/require"
)

func TestWakeable_DependencyErrorsCannotBecomeImplicitEligibility(t *testing.T) {
	for _, stage := range []string{"registration", "hub"} {
		t.Run(stage, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.register(t, "first")
			permissionRoot(t, f, "TEST-1")
			cause := errors.New("synthetic eligibility read failure")
			fault := permissionReadFailure{PermissionStore: f.store}
			if stage == "registration" {
				fault.registration = cause
			} else {
				fault.hub = cause
			}
			f.svc.Store = fault
			ok, _, err := f.svc.Wakeable(t.Context(), f.board, "first")
			require.ErrorIs(t, err, cause)
			require.False(t, ok)
			if stage == "registration" {
				ids, readErr := f.svc.PendingRegistrations(t.Context(), f.board)
				require.ErrorIs(t, readErr, cause)
				require.Nil(t, ids)
			}
			requirePermissionFixtureUnchanged(t, f)
		})
	}
}

func TestWakeable_MalformedProtectedGrantAndStrictSettingStayHeld(t *testing.T) {
	for _, name := range []string{"grants", "wake"} {
		t.Run(name, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.register(t, "first")
			_, err := f.store.MarkHubConnected(t.Context(), "config")
			require.NoError(t, err)
			value := map[string]any{"agent_grants": "invalid"}
			if name == "wake" {
				value = map[string]any{"inbox.strict_board_identity": "invalid"}
			}
			require.NoError(t, f.state.WriteJSON(name, value))
			dir, err := f.state.Path()
			require.NoError(t, err)
			path := filepath.Join(dir, name+".json")
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			ok, reason, err := f.svc.Wakeable(t.Context(), f.board, "first")
			require.Error(t, err)
			require.False(t, ok)
			require.Equal(t, "operator state unreadable", reason)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, before, after)
			if name == "grants" {
				ids, readErr := f.svc.PendingRegistrations(t.Context(), f.board)
				require.Error(t, readErr)
				require.Nil(t, ids)
			}
		})
	}
}

func TestCandidates_UntrustedInstructionDataCannotProducePartialFacts(t *testing.T) {
	for _, invalidIndex := range []bool{true, false} {
		t.Run(fmt.Sprint(invalidIndex), func(t *testing.T) {
			f := newWakePermissionFixture(t)
			f.register(t, "first")
			permissionRoot(t, f, "TEST-1")
			require.NoError(t, os.Mkdir(filepath.Join(f.board.Checkout, ".git"), 0700))
			index := permissionInstructionIndex("AGENTS.md")
			if invalidIndex {
				index = []byte("malformed synthetic Git index")
			}
			require.NoError(t, os.WriteFile(filepath.Join(f.board.Checkout, ".git", "index"), index, 0600))
			require.NoError(t, os.WriteFile(filepath.Join(f.board.Checkout, "AGENTS.md"), []byte("mtix agent register "+strings.Repeat("x", 65)), 0600))
			facts, err := f.svc.Candidates(t.Context(), f.board)
			require.Error(t, err)
			require.Nil(t, facts)
			requirePermissionFixtureUnchanged(t, f)
		})
	}
}

func TestApplyBatch_WholeRequestBoundsRejectBeforeAnyProtectedProjection(t *testing.T) {
	for _, name := range []string{"empty", "oversized", "duplicate", "invalid"} {
		t.Run(name, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			permissionRoot(t, f, "TEST-1")
			changes := []oplocal.PermissionChange{{AgentID: "first", Action: "allow"}}
			switch name {
			case "empty":
				changes = nil
			case "oversized":
				for i := range 1024 {
					changes = append(changes, oplocal.PermissionChange{AgentID: fmt.Sprintf("fixture-%04d", i), Action: "deny"})
				}
			case "duplicate":
				changes = append(changes, changes[0])
			case "invalid":
				changes[0].AgentID = strings.Repeat("x", 65)
			}
			err := f.svc.ApplyBatch(t.Context(), f.board, oplocal.PermissionBatch{Mode: "file", Source: "fixture bounds", Changes: changes})
			require.ErrorIs(t, err, model.ErrInvalidInput)
			requirePermissionFixtureUnchanged(t, f)
		})
	}
}
