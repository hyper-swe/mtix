// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

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

// TestMapError_HostileEventID_StatusMessageIsPrintableASCII checks that an
// error carrying a rendered event id keeps a printable-ASCII status message
// on every mapped code, including the Internal fallthrough, and that the
// original id is recoverable from the rendering.
func TestMapError_HostileEventID_StatusMessageIsPrintableASCII(t *testing.T) {
	sentinels := []error{
		model.ErrNotFound, model.ErrInvalidInput, model.ErrAlreadyExists,
		model.ErrConflict, model.ErrInvalidTransition, nil,
	}
	for _, id := range hostileEventIDs {
		for _, sentinel := range sentinels {
			t.Run(fmt.Sprintf("%s/%v", strconv.QuoteToASCII(id), sentinel), func(t *testing.T) {
				base := fmt.Errorf("event %s rejected", format.EventID(id))
				err := base
				if sentinel != nil {
					err = fmt.Errorf("%w: %w", sentinel, base)
				}
				st, ok := status.FromError(mapError(err))
				require.True(t, ok)
				assert.True(t, isPrintableASCII(st.Message()), "message %q", st.Message())
				assert.Contains(t, st.Message(), format.EventID(id))
				orig, uerr := strconv.Unquote(format.EventID(id))
				require.NoError(t, uerr)
				assert.Equal(t, id, orig)
			})
		}
	}
}

// TestMapError_RawEventID_IsDetected pins the oracle for the mutation check.
func TestMapError_RawEventID_IsDetected(t *testing.T) {
	for _, id := range hostileEventIDs {
		st, _ := status.FromError(mapError(fmt.Errorf("event %s rejected", id)))
		assert.False(t, isPrintableASCII(st.Message()), "%q", id)
	}
}
