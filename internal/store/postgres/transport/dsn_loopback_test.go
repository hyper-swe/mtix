// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsLoopback_HostForms_ReportsLoopbackOnlyForLoopbackHosts(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"", false},
		{"localhost", true},
		{"LOCALHOST", true},
		{"LocalHost", true},
		{"127.0.0.1", true},
		{"127.0.0.2", true},
		{"127.255.255.254", true},
		{"::1", true},
		{"0:0:0:0:0:0:0:1", true},
		{"::ffff:127.0.0.1", true},
		{"0.0.0.0", false},
		{"::", false},
		{"::1:5432", false},
		{"[::1]", false},
		{"10.0.0.1", false},
		{"db.example.com", false},
		{"localhost.example.com", false},
		{"127.0.0.1.example.com", false},
		{"/var/run/postgresql", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.host), func(t *testing.T) {
			require.Equal(t, tt.want, isLoopback(tt.host))
		})
	}
}
