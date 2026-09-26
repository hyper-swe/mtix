// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// registryIndexNotReadyWords and registryIndexNotValidWords say what an
// index that is not ready, and one that is ready but not valid, still does
// (MTIX-95.44).
const (
	registryIndexNotReadyWords = "it checks no new create"
	registryIndexNotValidWords = "it still refuses a duplicate create, but queries do not use it and it " +
		"must be built again"
)

// TestRegistryIndexNotUsable_EachState_SaysWhatTheIndexStillDoes: an index
// that is not ready checks no new create; one that is ready but not valid
// still refuses a duplicate create but must be built again (MTIX-95.44).
func TestRegistryIndexNotUsable_EachState_SaysWhatTheIndexStillDoes(t *testing.T) {
	tests := []struct {
		name         string
		valid, ready bool
		want, not    string
	}{
		{"not ready", false, false, registryIndexNotReadyWords, registryIndexNotValidWords},
		{"valid, not ready", true, false, registryIndexNotReadyWords, registryIndexNotValidWords},
		{"ready, not valid", false, true, registryIndexNotValidWords, registryIndexNotReadyWords},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := registryIndexNotUsable(transport.RegistryIndexState{Present: true, Valid: tt.valid, Ready: tt.ready,
				SchemaIdent: "public"})
			require.Contains(t, got, tt.want)
			require.NotContains(t, got, tt.not)
		})
	}
}

// TestRegistryIndexHelp_DoctorAndInit_WordBothStates: the doctor's and
// init's help, and the agent docs, describe both states truthfully and
// never say that an index that is only not valid checks no new create
// (MTIX-95.44).
func TestRegistryIndexHelp_DoctorAndInit_WordBothStates(t *testing.T) {
	texts := map[string]string{"doctor help": newSyncDoctorCmd().Long, "init help": newSyncInitCmd().Long}
	for _, rel := range []string{"USERMANUAL.md", "internal/docs/templates/skills/admin.md.tmpl",
		".claude-plugin/skills/mtix-admin.md", ".codex-plugin/skills/admin/SKILL.md"} {
		body, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, strings.Split(rel, "/")...)...))
		require.NoError(t, err)
		texts[rel] = string(body)
	}
	for name, text := range texts {
		flat := strings.Join(strings.Fields(text), " ")
		require.Containsf(t, flat, "not ready checks no new create", "%s: the not-ready state", name)
		require.Containsf(t, flat, "ready but not valid still refuses a duplicate create, but queries do not use "+
			"it and it must be built again", "%s: the ready but not valid state", name)
		require.NotContainsf(t, flat, "not valid or not ready fails the check in every mode: it checks no new",
			"%s", name)
	}
}
