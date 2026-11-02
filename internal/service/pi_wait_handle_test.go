// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/service"
)

// Every handle that agent registration accepts, the wait verb accepts too, and every one it
// rejects the wait verb rejects (the empty handle aside: it registers but cannot wait).
func TestSessionHandle_RegistrableIsWaitable(t *testing.T) {
	handles := []string{
		"01a13d60-79cf-7000-8000-aaaaaaaaaaaa", "/tmp/cc-socks/a b.sock", "sessión", "-x", "$(id)", "a;b",
		strings.Repeat("a", attach.MaxSessionLen), strings.Repeat("a", attach.MaxSessionLen+1),
		"a\nb", "a\x00b", "a\tb", "a b",
	}
	for i, h := range handles {
		t.Run(fmt.Sprintf("case %d", i), func(t *testing.T) {
			f := newRegFixture(t)
			_, regErr := f.svc.RegisterSession(context.Background(), service.RegisterSessionRequest{
				AgentID: "w", Harness: "pi", SessionHandle: h, Origin: "manual"})
			waitErr := attach.ValidateSession(h)
			assert.Equal(t, regErr == nil, waitErr == nil, "register err %v, wait err %v", regErr, waitErr)
		})
	}
	require.Error(t, attach.ValidateSession(""), "no handle, nothing to wait for")
}
