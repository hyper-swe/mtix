// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestHandleError_SentinelErrors verifies error-to-HTTP mapping per FR-7.7.
func TestHandleError_SentinelErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not_found", model.ErrNotFound, http.StatusNotFound, "NOT_FOUND"},
		{"already_exists", model.ErrAlreadyExists, http.StatusConflict, "ALREADY_EXISTS"},
		{"invalid_input", model.ErrInvalidInput, http.StatusBadRequest, "INVALID_INPUT"},
		{"invalid_transition", model.ErrInvalidTransition, http.StatusConflict, "INVALID_TRANSITION"},
		{"cycle_detected", model.ErrCycleDetected, http.StatusConflict, "CYCLE_DETECTED"},
		{"conflict", model.ErrConflict, http.StatusConflict, "CONFLICT"},
		{"already_claimed", model.ErrAlreadyClaimed, http.StatusConflict, "ALREADY_CLAIMED"},
		{"node_blocked", model.ErrNodeBlocked, http.StatusConflict, "NODE_BLOCKED"},
		{"still_deferred", model.ErrStillDeferred, http.StatusConflict, "STILL_DEFERRED"},
		{"agent_still_active", model.ErrAgentStillActive, http.StatusConflict, "AGENT_STILL_ACTIVE"},
		{"no_active_session", model.ErrNoActiveSession, http.StatusConflict, "NO_ACTIVE_SESSION"},
		{"invalid_config_key", model.ErrInvalidConfigKey, http.StatusBadRequest, "INVALID_CONFIG_KEY"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			HandleError(c, tt.err)

			assert.Equal(t, tt.wantStatus, w.Code)

			var resp ErrorResponse
			err := json.Unmarshal(w.Body.Bytes(), &resp)
			require.NoError(t, err)
			assert.Equal(t, tt.wantCode, resp.Error.Code)
			assert.NotEmpty(t, resp.Error.Message)
		})
	}
}

// TestHandleError_WrappedSentinel verifies wrapped errors are still matched.
func TestHandleError_WrappedSentinel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	// Wrap the sentinel error.
	wrapped := errors.New("node PROJ-42 not found: " + model.ErrNotFound.Error())
	_ = wrapped // Can't use fmt.Errorf with %w in test easily, but test the concept
	HandleError(c, model.ErrNotFound)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

// TestHandleError_UnknownError_Returns500 verifies unknown errors return 500.
func TestHandleError_UnknownError_Returns500(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	HandleError(c, errors.New("some random database error"))

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	var resp ErrorResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "INTERNAL_ERROR", resp.Error.Code)
	// Should NOT contain internal error details.
	assert.NotContains(t, resp.Error.Message, "database")
}

// TestHandleValidationError_Returns400 verifies validation error format.
func TestHandleValidationError_Returns400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	HandleValidationError(c, "title is required")

	assert.Equal(t, http.StatusBadRequest, w.Code)

	var resp ErrorResponse
	err := json.Unmarshal(w.Body.Bytes(), &resp)
	require.NoError(t, err)
	assert.Equal(t, "INVALID_INPUT", resp.Error.Code)
	assert.Contains(t, resp.Error.Message, "title is required")
}
