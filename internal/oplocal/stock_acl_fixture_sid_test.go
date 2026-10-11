// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Portable fixture SID validation checks canonical local administrator identity.
// Supplemental semantic controls do not execute native SID APIs or ACL policy.
package oplocal

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStockFixtureLocalAdministratorSID_SYNTHETICControls_RequiresCanonicalAccount(t *testing.T) {
	tests := []struct {
		name, sid string
		valid     bool
	}{
		{"historical identity shape", "S-1-5-21-1643835476-1616584234-1346609752-500", true},
		{"alternate host", "S-1-5-21-1-2-3-500", true},
		{"zero domain fields", "S-1-5-21-0-0-0-500", true},
		{"maximum domain fields", "S-1-5-21-4294967295-4294967295-4294967295-500", true},
		{"empty", "", false},
		{"alias only", "LA", false},
		{"wrong revision", "S-2-5-21-1-2-3-500", false},
		{"wrong authority", "S-1-6-21-1-2-3-500", false},
		{"builtin group", "S-1-5-32-1-2-3-500", false},
		{"missing domain field", "S-1-5-21-1-2-500", false},
		{"extra domain field", "S-1-5-21-1-2-3-4-500", false},
		{"missing RID", "S-1-5-21-1-2-3", false},
		{"unrelated RID", "S-1-5-21-1-2-3-1001", false},
		{"RID suffix decoy", "S-1-5-21-1-2-3-5000", false},
		{"noncanonical RID", "S-1-5-21-1-2-3-0500", false},
		{"leading zero", "S-1-5-21-01-2-3-500", false},
		{"zero padded", "S-1-5-21-00-2-3-500", false},
		{"positive sign", "S-1-5-21-+1-2-3-500", false},
		{"negative sign", "S-1-5-21--1-2-3-500", false},
		{"leading whitespace", " S-1-5-21-1-2-3-500", false},
		{"trailing whitespace", "S-1-5-21-1-2-3-500 ", false},
		{"embedded whitespace", "S-1-5-21-1 -2-3-500", false},
		{"nonnumeric domain", "S-1-5-21-x-2-3-500", false},
		{"overflow first", "S-1-5-21-4294967296-2-3-500", false},
		{"overflow second", "S-1-5-21-1-4294967296-3-500", false},
		{"overflow third", "S-1-5-21-1-2-4294967296-500", false},
		{"huge decimal", "S-1-5-21-18446744073709551616-2-3-500", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := stockFixtureLocalAdministratorSIDError(test.sid)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func stockFixtureLocalAdministratorSIDError(sid string) error {
	parts := strings.Split(sid, "-")
	if len(parts) != 8 || strings.Join(parts[:4], "-") != "S-1-5-21" || parts[7] != "500" {
		return fmt.Errorf("local administrator SID must have exact S-1-5-21-A-B-C-500 shape")
	}
	for _, part := range parts[4:7] {
		value, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return fmt.Errorf("parse local administrator domain subauthority: %w", err)
		}
		if strconv.FormatUint(value, 10) != part {
			return fmt.Errorf("local administrator domain subauthority must use canonical decimal")
		}
	}
	return nil
}
