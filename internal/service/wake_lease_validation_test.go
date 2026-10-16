// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type wakeLeaseInvalidRequest struct {
	name       string
	key        service.WakeLeaseKey
	holder     string
	ttl        time.Duration
	holderOnly bool
	message    string
}

func wakeLeaseInvalidRequests() []wakeLeaseInvalidRequest {
	key := wakeLeaseServiceKey()
	identity := "wake lease identity required"
	token := "wake lease holder must be 32 lowercase hex characters"
	return []wakeLeaseInvalidRequest{
		{"agent missing", service.WakeLeaseKey{HostID: key.HostID, UID: key.UID}, wakeLeaseTestHolder, time.Minute, false, identity},
		{"host missing", service.WakeLeaseKey{AgentID: key.AgentID, UID: key.UID}, wakeLeaseTestHolder, time.Minute, false, identity},
		{"user missing", service.WakeLeaseKey{AgentID: key.AgentID, HostID: key.HostID}, wakeLeaseTestHolder, time.Minute, false, identity},
		{"ttl too short", key, wakeLeaseTestHolder, time.Minute - time.Nanosecond, false, "wake lease ttl must be between 1m and 24h"},
		{"ttl too long", key, wakeLeaseTestHolder, 24*time.Hour + time.Nanosecond, false, "wake lease ttl must be between 1m and 24h"},
		{"holder missing", key, "", time.Minute, true, "required"},
		{"holder short", key, strings.Repeat("a", 31), time.Minute, true, token},
		{"holder long", key, strings.Repeat("a", 33), time.Minute, true, token},
		{"holder uppercase", key, strings.Repeat("A", 32), time.Minute, true, token},
		{"holder nonhex", key, strings.Repeat("g", 32), time.Minute, true, token},
		{"identity first", service.WakeLeaseKey{}, "wrong", time.Second, false, identity},
		{"ttl before holder", key, "wrong", time.Second, false, "wake lease ttl must be between 1m and 24h"},
	}
}

func TestWakeLeaseService_InvalidRequest_StopsBeforeBackend(t *testing.T) {
	for _, tc := range wakeLeaseInvalidRequests() {
		for index := range 4 {
			probe := &wakeLeaseBackendProbe{}
			op := wakeLeaseOperations(service.NewWakeLeaseService(probe), tc.holder)[index]
			if tc.holderOnly && op.name == "acquire" {
				continue
			}
			t.Run(op.name+"/"+tc.name, func(t *testing.T) {
				_, err := op.run(t.Context(), tc.key, tc.ttl)
				require.ErrorIs(t, err, model.ErrInvalidInput)
				message := tc.message
				if message == "required" {
					verb := "release"
					if op.name == "renew" {
						verb = "renew"
					}
					message = verb + " wake lease holder required"
				}
				assert.Equal(t, message+": "+model.ErrInvalidInput.Error(), err.Error())
				assert.Empty(t, probe.calls, "invalid input must not reach persistence")
			})
		}
	}
}

func TestParseWakeLeaseTTL_Boundaries_PreservesCLIContract(t *testing.T) {
	for _, tc := range []struct {
		input   string
		want    time.Duration
		message string
	}{
		{"1m", time.Minute, ""}, {"24h", 24 * time.Hour, ""}, {"15m", 15 * time.Minute, ""},
		{"59s", 0, "lease ttl must be between 1m and 24h: invalid input"},
		{"24h1ns", 0, "lease ttl must be between 1m and 24h: invalid input"},
		{"wrong", 0, "invalid lease ttl \"wrong\" (time: invalid duration \"wrong\"): invalid input"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			got, err := service.ParseWakeLeaseTTL(tc.input)
			assert.Equal(t, tc.want, got)
			if tc.message == "" {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				assert.Equal(t, tc.message, err.Error())
			}
		})
	}
}
