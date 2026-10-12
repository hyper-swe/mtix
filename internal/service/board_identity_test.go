// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

// Committed identity creation is exclusive and readers never repair invalid input.

import (
	"errors"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestCreateBoardID_ConcurrentCreation_PreservesOneIdentity(t *testing.T) {
	dir := t.TempDir()
	var workers sync.WaitGroup
	ids := make(chan string, 8)
	failures := make(chan error, 8)
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			id, err := CreateBoardID(dir)
			if err != nil {
				failures <- err
			} else {
				ids <- id
			}
		}()
	}
	workers.Wait()
	close(ids)
	close(failures)
	require.Len(t, ids, 1)
	require.Len(t, failures, 7)
	for err := range failures {
		assert.ErrorIs(t, err, os.ErrExist)
	}
	stored, err := ReadBoardIDs(dir)
	require.NoError(t, err)
	assert.Equal(t, []string{<-ids}, stored)
	_, err = CreateBoardID(filepath.Join(dir, "absent"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestReadBoardIDs_InvalidAndConflictContents_AreNotRewritten(t *testing.T) {
	first := "0c6f03d9-3633-4ab7-bcc9-b8da8d966023"
	second := "4549495e-f331-4ac3-86af-decd9cbcbda6"
	cases := []struct {
		name, raw string
		want      []string
		invalid   bool
	}{
		{name: "Single", raw: first + "\n", want: []string{first}},
		{name: "Distinct", raw: first + "\n" + second + "\n", want: []string{first, second}},
		{name: "Conflict", raw: "<<<<<<< current\n" + first + "\n=======\n" + second + "\n>>>>>>> incoming\n", want: []string{first, second}},
		{name: "Empty", invalid: true},
		{name: "MissingNewline", raw: first, invalid: true},
		{name: "Repeated", raw: first + "\n" + first + "\n", invalid: true},
		{name: "Malformed", raw: "invalid\n", invalid: true},
		{name: "Noncanonical", raw: strings.ToUpper(first) + "\n", invalid: true},
		{name: "Zero", raw: "00000000-0000-0000-0000-000000000000\n", invalid: true},
		{name: "Limit", raw: strings.Repeat("x", 4097), invalid: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "board-id")
			require.NoError(t, os.WriteFile(path, []byte(tc.raw), 0600))
			ids, err := ReadBoardIDs(dir)
			if tc.invalid {
				assert.ErrorIs(t, err, model.ErrInvalidInput)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, ids)
			}
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			assert.Equal(t, tc.raw, string(after))
		})
	}
}

func TestReadBoardIDs_MissingAndNonregularFiles_ReturnCauses(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadBoardIDs(dir)
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "board-id"), 0700))
	_, err = ReadBoardIDs(dir)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	require.NoError(t, os.Remove(filepath.Join(dir, "board-id")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "real"), []byte("kept"), 0600))
	if err = os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "board-id")); err == nil {
		_, err = ReadBoardIDs(dir)
		assert.True(t, errors.Is(err, model.ErrInvalidInput))
	}
}
