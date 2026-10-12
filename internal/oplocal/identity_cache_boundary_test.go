// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package oplocal

// Cache validation retains prior observations when input or persistence is refused.

import (
	"errors"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"sync"
	"testing"
)

func TestObserveBoard_InvalidInputAndCommitFailure_PreservesPreviousRecord(t *testing.T) {
	s := identityState(t)
	path := t.TempDir()
	known := HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}
	require.NoError(t, s.ObserveBoard(path, testBoardID, known))
	before, _, err := s.BoardObservation(path)
	require.NoError(t, err)
	for _, input := range []struct {
		path, id string
		hub      HubIdentity
	}{{"relative", testBoardID, known}, {filepath.Join(path, "child") + "/..", testBoardID, known}, {path, "invalid", known}, {path, testBoardID, HubIdentity{Kind: "future"}}} {
		assert.ErrorIs(t, s.ObserveBoard(input.path, input.id, input.hub), model.ErrOperatorStateUnreadable)
	}
	failure := errors.New("commit refused")
	s.beforeCommit = func() error { return failure }
	err = s.ObserveBoard(path, testBoardID, HubIdentity{})
	assert.ErrorIs(t, err, failure)
	assert.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	s.beforeCommit = nil
	after, _, err := s.BoardObservation(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestObserveBoard_ConcurrentCheckouts_RetainIndependentRecords(t *testing.T) {
	s := identityState(t)
	root := t.TempDir()
	var workers sync.WaitGroup
	failures := make(chan error, 8)
	for i := range 8 {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			failures <- s.ObserveBoard(filepath.Join(root, string(rune('a'+index))), testBoardID, HubIdentity{})
		}(i)
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	for i := range 8 {
		path := filepath.Join(root, string(rune('a'+i)))
		record, report, err := s.BoardObservation(path)
		require.NoError(t, err)
		require.Empty(t, report)
		assert.Equal(t, path, record.Checkout)
	}
}

func TestBoardObservation_InvalidStoredMetadata_ReturnsValidationError(t *testing.T) {
	s := identityState(t)
	path := t.TempDir()
	name, err := boardRecordName(path)
	require.NoError(t, err)
	for _, record := range []BoardObservation{{Checkout: filepath.Join(path, "other")}, {Checkout: path, BoardID: "invalid"}, {Checkout: path, Hub: HubIdentity{Kind: "future"}}} {
		require.NoError(t, s.WriteJSON(name, record))
		_, _, err = s.BoardObservation(path)
		assert.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	}
	_, _, err = s.BoardObservation("relative")
	assert.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestBoardGrants_InvalidStoredValues_ReturnsValidationError(t *testing.T) {
	s := identityState(t)
	for _, grant := range []Grant{{BoardID: "invalid"}, {BoardID: testBoardID, Hub: HubIdentity{Kind: "future"}}} {
		require.NoError(t, s.WriteJSON("grants", map[string]any{"board_grants": []Grant{grant}}))
		_, _, err := s.BoardGrants()
		assert.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	}
}

func TestStrictBoardIdentity_NullAndUnreadableState_ReturnsValidationError(t *testing.T) {
	s := identityState(t)
	require.NoError(t, s.WriteJSON("wake", map[string]any{"inbox.strict_board_identity": nil}))
	_, _, err := s.StrictBoardIdentity()
	assert.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.NoError(t, s.WriteJSON("wake", []string{"invalid"}))
	_, _, err = s.StrictBoardIdentity()
	assert.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
	require.NoError(t, s.WriteJSON("grants", []string{"invalid"}))
	_, _, err = s.BoardGrants()
	assert.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
}

func TestObserveRelay_ValidAndInvalidConfiguration_PreservesValidationBoundary(t *testing.T) {
	s := identityState(t)
	checkout := t.TempDir()
	dir := t.TempDir()
	relayID := "019b8e44-42b3-7000-8000-000000000001"
	require.NoError(t, s.ObserveRelay(checkout, testBoardID, dir, relayID))
	got, _, err := s.BoardObservation(checkout)
	require.NoError(t, err)
	assert.Equal(t, dir, got.RelayPath)
	assert.Equal(t, relayID, got.Hub.RelayID)
	for _, args := range [][4]string{{"relative", testBoardID, dir, relayID}, {checkout, testBoardID, "relative", relayID}, {checkout, testBoardID, dir, "invalid"}, {checkout, "invalid", dir, relayID}} {
		assert.ErrorIs(t, s.ObserveRelay(args[0], args[1], args[2], args[3]), model.ErrOperatorStateUnreadable)
	}
}

func TestBoardObservation_IncompleteRecord_ReturnsErrorWithoutRepair(t *testing.T) {
	state := identityState(t)
	checkout := t.TempDir()
	name, err := boardRecordName(checkout)
	require.NoError(t, err)
	records := []BoardObservation{{BoardID: testBoardID}, {Hub: HubIdentity{Kind: "postgres", ProjectPrefix: "TEST", FirstEventHash: "first"}}, {RelayPath: checkout}}
	for _, record := range records {
		require.NoError(t, state.WriteJSON(name, record))
		_, _, err = state.BoardObservation(checkout)
		assert.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		var after BoardObservation
		_, readErr := state.ReadJSON(name, &after)
		require.NoError(t, readErr)
		assert.Equal(t, record, after)
	}
}
