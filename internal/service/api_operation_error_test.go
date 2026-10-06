// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestAPIOperationError_PreservesMessageContextAndChain(t *testing.T) {
	cause := fmt.Errorf("original storage context: %w", model.ErrConflict)
	err := wrapAPIOperation("claim work", cause)
	require.Equal(t, cause.Error(), err.Error())
	require.ErrorIs(t, err, model.ErrConflict)
	require.Same(t, cause, errors.Unwrap(errors.Unwrap(err)))
	require.Equal(t, "claim work: "+cause.Error(), errors.Unwrap(err).Error())
	var operation interface{ Operation() string }
	require.ErrorAs(t, err, &operation)
	require.Equal(t, "claim work", operation.Operation())
}
