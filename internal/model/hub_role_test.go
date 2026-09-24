// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestValidHubRoleName_Forms_MatchesIdentifierRule pins the role-name form
// mtix accepts in hub statements (SQL Rule 1a, MTIX-95.1).
func TestValidHubRoleName_Forms_MatchesIdentifierRule(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"lowercase", "mtix_team", true},
		{"leading underscore", "_svc", true},
		{"digits after first", "team2", true},
		{"63 bytes", "a" + strings.Repeat("b", 62), true},
		{"64 bytes", "a" + strings.Repeat("b", 63), false},
		{"empty", "", false},
		{"uppercase", "Team", false},
		{"leading digit", "2team", false},
		{"hyphen", "mtix-team", false},
		{"quote", `te"am`, false},
		{"space", "te am", false},
		{"non-ascii letter", "t\u00e9am", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, model.ValidHubRoleName(tt.in))
		})
	}
}

// TestDataAPIRoles_Names_AreAnonymousAndSignedIn pins the data-API role
// names (ADR-006 D26, MTIX-95.1).
func TestDataAPIRoles_Names_AreAnonymousAndSignedIn(t *testing.T) {
	require.Equal(t, []string{"anon", "authenticated"}, model.DataAPIRoles())
}

// TestParseKeepRoles_Values_ValidatesEachName covers the kept-role list
// accepted from --keep-role and the sync.keep_roles key (MTIX-95.1).
func TestParseKeepRoles_Values_ValidatesEachName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr string
	}{
		{"empty is none", "", nil, ""},
		{"blank is none", "  ", nil, ""},
		{"one role", "mtix_team", []string{"mtix_team"}, ""},
		{"list trims, sorts and de-duplicates", " team_b , team_a,team_b ", []string{"team_a", "team_b"}, ""},
		{"empty entry", "team_a,,team_b", nil, "empty role name"},
		{"public refused", "team_a,public", nil, "cannot be kept"},
		{"public any case refused", "PUBLIC", nil, "cannot be kept"},
		{"anonymous data-API role refused", "anon", nil, "cannot be kept"},
		{"signed-in data-API role refused", "authenticated", nil, "cannot be kept"},
		{"predefined role refused", "pg_read_all_data", nil, "cannot be kept"},
		{"invalid name refused", "Team", nil, "not a valid role name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := model.ParseKeepRoles(tt.in)
			if tt.wantErr != "" {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestValidateKeepRoles_List_RefusesTheSameNamesAsParse checks that a list
// built from repeated --keep-role flags follows the same rules as the
// comma-separated config value (MTIX-95.1).
func TestValidateKeepRoles_List_RefusesTheSameNamesAsParse(t *testing.T) {
	got, err := model.ValidateKeepRoles([]string{"team_b", "team_a", "team_b"})
	require.NoError(t, err)
	require.Equal(t, []string{"team_a", "team_b"}, got)

	for _, bad := range []string{"anon", "public", "pg_write_all_data", "x-y", ""} {
		_, err := model.ValidateKeepRoles([]string{"team_a", bad})
		require.ErrorIs(t, err, model.ErrInvalidInput, bad)
	}
}
