// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Local create boundaries share FR-2.1a prefix validation (MTIX-107.3).
package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestCreateCLI_PrefixGrammar_ReturnsSharedError(t *testing.T) {
	for _, prefix := range []string{"TEST_BAD", "test", "1TEST", "ABCDEFGHIJKLMNOPQRSTU", "TEST%", "A", "TEST-DEV-OPS", "ABCDEFGHIJKLMNOPQRST", ""} {
		t.Run(prefix, func(t *testing.T) {
			initTestApp(t)
			err := runCreateWithProject("Prefix boundary", "", "", 3, "", "", "", "", "", prefix, true)
			if expected := model.ValidatePrefix(prefix); prefix != "" && expected != nil {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				assert.EqualError(t, err, expected.Error())
				return
			}
			require.NoError(t, err)
			if prefix == "" {
				prefix = "TEST"
			}
			n, err := app.store.GetNode(t.Context(), prefix+"-1")
			require.NoError(t, err)
			assert.Equal(t, prefix, n.Project)
		})
	}
}

func TestCreateCLI_InvalidInheritedPrefix_ReturnsSharedError(t *testing.T) {
	initTestApp(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Sync accepts historical underscore prefixes; local child creation must not.
	require.NoError(t, app.store.CreateNode(t.Context(), &model.Node{
		ID: "TEST_BAD-1", Project: "TEST_BAD", Seq: 1, Title: "Historical sync parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1,
		NodeType: model.NodeTypeEpic, CreatedAt: now, UpdatedAt: now,
	}))
	err := runCreateWithProject("Local child", "TEST_BAD-1", "", 3, "", "", "", "", "", "", true)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.EqualError(t, err, model.ValidatePrefix("TEST_BAD").Error())
	_, err = app.store.GetNode(t.Context(), "TEST_BAD-1.1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

func TestInit_InvalidPrefix_ReturnsSharedErrorWithoutFiles(t *testing.T) {
	for _, prefix := range []string{"TEST_BAD", "test", "", "1TEST", "ABCDEFGHIJKLMNOPQRSTU", "TEST%"} {
		t.Run(prefix, func(t *testing.T) {
			t.Chdir(t.TempDir())
			err := runInit(prefix)
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.EqualError(t, err, model.ValidatePrefix(prefix).Error())
			_, statErr := os.Stat(filepath.Join(".mtix", "config.yaml"))
			assert.True(t, os.IsNotExist(statErr))
		})
	}
}

func TestInit_ValidPrefixBoundary_WritesConfig(t *testing.T) {
	for _, prefix := range []string{"A", "TEST-DEV-OPS", "ABCDEFGHIJKLMNOPQRST"} {
		t.Run(prefix, func(t *testing.T) {
			saveAndResetApp(t)
			t.Chdir(t.TempDir())
			require.NoError(t, runInit(prefix))
			content, err := os.ReadFile(filepath.Join(".mtix", "config.yaml"))
			require.NoError(t, err)
			assert.Contains(t, string(content), prefix)
		})
	}
}
