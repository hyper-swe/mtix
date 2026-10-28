// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package http

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// hostileEventIDs are stored sync event ids that must never reach an error
// or response text raw (MTIX-187.4.5).
var hostileEventIDs = []string{
	"evt\x1b[31mred\x1b[0m",
	"evt\nINJECTED: line",
	"evt\x00nul",
	"evt-\u202egnp.exe",
	"evt-\u00e9\u4e2d",
}

func isPrintableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// TestHandleError_HostileEventID_ResponseIsASCIIAndRoundTrips checks that an
// error carrying a rendered event id reaches the client as printable ASCII
// and that the id round-trips from the rendering unchanged.
func TestHandleError_HostileEventID_ResponseIsASCIIAndRoundTrips(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, id := range hostileEventIDs {
		t.Run(strconv.QuoteToASCII(id), func(t *testing.T) {
			err := fmt.Errorf("event %s: %w", format.EventID(id), model.ErrInvalidInput)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			HandleError(c, err)

			assert.True(t, isPrintableASCII(w.Body.String()),
				"raw bytes must be printable ASCII: %q", w.Body.String())
			var resp ErrorResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Equal(t, err.Error(), resp.Error.Message)
			assert.Contains(t, resp.Error.Message, format.EventID(id))
		})
	}
}

// TestHandleError_RawEventIDInMessage_IsDetected pins the test oracle: a raw
// id in a message is JSON-encoded on the wire (valid) but fails the
// ASCII check once decoded, which is what a bypass of the helper would do.
func TestHandleError_RawEventIDInMessage_IsDetected(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, id := range hostileEventIDs {
		err := fmt.Errorf("event %s: %w", id, model.ErrInvalidInput)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		HandleError(c, err)
		var resp ErrorResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		assert.False(t, isPrintableASCII(resp.Error.Message), "decoded message of %q", id)
	}
}

// TestHandleError_UnknownErrorWithEventID_DoesNotEchoText confirms the 500
// path never repeats error text, so an event id cannot appear there.
func TestHandleError_UnknownErrorWithEventID_DoesNotEchoText(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, id := range hostileEventIDs {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		HandleError(c, fmt.Errorf("event %s failed", id))
		assert.True(t, isPrintableASCII(w.Body.String()))
		assert.NotContains(t, w.Body.String(), "event")
	}
}
