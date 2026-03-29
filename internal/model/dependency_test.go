// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestDependency_Validate_ValidDep_NoError verifies a well-formed dependency passes.
func TestDependency_Validate_ValidDep_NoError(t *testing.T) {
	dep := &model.Dependency{
		FromID:  "PROJ-1",
		ToID:    "PROJ-2",
		DepType: model.DepTypeBlocks,
	}

	err := dep.Validate()
	assert.NoError(t, err)
}

// TestDependency_Validate_InvalidDepType_ReturnsError verifies unrecognized types are rejected.
func TestDependency_Validate_InvalidDepType_ReturnsError(t *testing.T) {
	dep := &model.Dependency{
		FromID:  "PROJ-1",
		ToID:    "PROJ-2",
		DepType: model.DepType("invalid"),
	}

	err := dep.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestDependency_Validate_SelfReference_ReturnsError verifies self-referencing is rejected.
func TestDependency_Validate_SelfReference_ReturnsError(t *testing.T) {
	dep := &model.Dependency{
		FromID:  "PROJ-1",
		ToID:    "PROJ-1",
		DepType: model.DepTypeBlocks,
	}

	err := dep.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "self-referencing")
}

// TestDependency_Validate_EmptyFromID_ReturnsError verifies empty from_id is rejected.
func TestDependency_Validate_EmptyFromID_ReturnsError(t *testing.T) {
	dep := &model.Dependency{
		FromID:  "",
		ToID:    "PROJ-2",
		DepType: model.DepTypeBlocks,
	}

	err := dep.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestDependency_Validate_EmptyToID_ReturnsError verifies empty to_id is rejected.
func TestDependency_Validate_EmptyToID_ReturnsError(t *testing.T) {
	dep := &model.Dependency{
		FromID:  "PROJ-1",
		ToID:    "",
		DepType: model.DepTypeBlocks,
	}

	err := dep.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestDependency_Validate_AllValidTypes verifies all valid dep types pass.
func TestDependency_Validate_AllValidTypes(t *testing.T) {
	validTypes := []model.DepType{
		model.DepTypeBlocks,
		model.DepTypeRelated,
		model.DepTypeDiscoveredFrom,
		model.DepTypeDuplicates,
	}

	for _, dt := range validTypes {
		t.Run(string(dt), func(t *testing.T) {
			dep := &model.Dependency{
				FromID:  "PROJ-1",
				ToID:    "PROJ-2",
				DepType: dt,
			}
			assert.NoError(t, dep.Validate())
		})
	}
}
