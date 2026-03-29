// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hyper-swe/mtix/internal/model"
)

// ErrorResponse is the standard error response format per FR-7.7.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains error code and message per FR-7.7.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// sentinelMapping maps sentinel errors to HTTP status codes and error codes.
var sentinelMapping = []struct {
	err    error
	status int
	code   string
}{
	{model.ErrNotFound, http.StatusNotFound, "NOT_FOUND"},
	{model.ErrAlreadyExists, http.StatusConflict, "ALREADY_EXISTS"},
	{model.ErrInvalidInput, http.StatusBadRequest, "INVALID_INPUT"},
	{model.ErrInvalidTransition, http.StatusConflict, "INVALID_TRANSITION"},
	{model.ErrCycleDetected, http.StatusConflict, "CYCLE_DETECTED"},
	{model.ErrConflict, http.StatusConflict, "CONFLICT"},
	{model.ErrAlreadyClaimed, http.StatusConflict, "ALREADY_CLAIMED"},
	{model.ErrNodeBlocked, http.StatusConflict, "NODE_BLOCKED"},
	{model.ErrStillDeferred, http.StatusConflict, "STILL_DEFERRED"},
	{model.ErrAgentStillActive, http.StatusConflict, "AGENT_STILL_ACTIVE"},
	{model.ErrNoActiveSession, http.StatusConflict, "NO_ACTIVE_SESSION"},
	{model.ErrInvalidConfigKey, http.StatusBadRequest, "INVALID_CONFIG_KEY"},
}

// HandleError sends a structured error response per FR-7.7.
// Maps sentinel errors to appropriate HTTP status codes.
// Unknown errors return 500 without leaking internal details.
func HandleError(c *gin.Context, err error) {
	for _, m := range sentinelMapping {
		if errors.Is(err, m.err) {
			c.JSON(m.status, ErrorResponse{
				Error: ErrorDetail{
					Code:    m.code,
					Message: err.Error(),
				},
			})
			return
		}
	}

	// Unknown error — return 500 without leaking internals.
	c.JSON(http.StatusInternalServerError, ErrorResponse{
		Error: ErrorDetail{
			Code:    "INTERNAL_ERROR",
			Message: "an internal error occurred",
		},
	})
}

// HandleValidationError sends a 400 error for request validation failures.
func HandleValidationError(c *gin.Context, message string) {
	c.JSON(http.StatusBadRequest, ErrorResponse{
		Error: ErrorDetail{
			Code:    "INVALID_INPUT",
			Message: message,
		},
	})
}
