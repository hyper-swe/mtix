// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package oplocal

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func fullConfirmationSegment(t *testing.T, host string) []byte {
	t.Helper()
	batch := permissionFixtureBatch()
	batch.Source = "rotation fixture"
	for i := range 128 {
		batch.Markers = append(batch.Markers, fmt.Sprintf("FIXTURE_%03d", i))
	}
	row := batchConfirmations(batch, host)[0]
	row.BoardLine = "published"
	rows := []Confirmation{}
	for i := 0; ; i++ {
		row.ID = fmt.Sprintf("%032x", i+100)
		next := append(append([]Confirmation{}, rows...), row)
		data, err := encodeConfirmations(next)
		if err != nil {
			data, err = encodeConfirmations(rows)
			require.NoError(t, err)
			return data
		}
		rows = next
		require.LessOrEqual(t, len(data), maxRecordSize)
	}
}
func seedConfirmationSegment(t *testing.T, s *State, index int, data []byte) {
	t.Helper()
	dir, err := s.directory(true)
	require.NoError(t, err)
	require.NoError(t, writeData(dir, confirmationFilename(index), data, false, nil, nil))
	require.NoError(t, closeFile(dir))
}
func TestPermissions_RotationRecoveryKeepsThreeSegments(t *testing.T) {
	for _, stage := range []string{"after:confirmations.2.jsonl", "after:confirmations.1.jsonl", "after:confirmations.jsonl"} {
		t.Run(stage, func(t *testing.T) { testPermissionRotation(t, stage) })
	}
}
func testPermissionRotation(t *testing.T, stage string) {
	t.Helper()
	s := alertFixture(t)
	batch := permissionFixtureBatch()
	require.NoError(t, s.ApplyPermissions(t.Context(), batch, nil))
	host, err := s.HostID()
	require.NoError(t, err)
	active := fullConfirmationSegment(t, host)
	seedConfirmationSegment(t, s, 0, active)
	oldBatch := permissionFixtureBatch()
	oldBatch.ID = "22222222222222222222222222222222"
	old, err := encodeConfirmations(batchConfirmations(oldBatch, host))
	require.NoError(t, err)
	seedConfirmationSegment(t, s, 1, old)
	batch.ID = "11111111111111111111111111111111"
	for i := range 8 {
		batch.Changes = append(batch.Changes, PermissionChange{AgentID: fmt.Sprintf("rotation-%d", i), Action: "deny"})
	}
	fired := false
	s.alertStage = func(at string) error {
		if at == stage && !fired {
			fired = true
			return errors.New("rotation fault")
		}
		return nil
	}
	require.Error(t, s.ApplyPermissions(t.Context(), batch, nil))
	require.True(t, fired)
	require.NoError(t, New(s.env).ApplyPermissions(t.Context(), batch, nil))
	dir, err := s.directory(false)
	require.NoError(t, err)
	defer func() { require.NoError(t, closeFile(dir)) }()
	segments, err := readConfirmationSegments(dir, host)
	require.NoError(t, err)
	require.Len(t, segments[0].rows, 9)
	require.Equal(t, active, segments[1].data)
	require.Equal(t, old, segments[2].data)
	for _, seg := range segments {
		require.LessOrEqual(t, len(seg.data), maxRecordSize)
	}
	_, err = readData(dir, "confirmations.3.jsonl")
	require.ErrorIs(t, err, os.ErrNotExist)
}
