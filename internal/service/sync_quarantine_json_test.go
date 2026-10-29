// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

// These tests pin the CLI JSON contract and keep storage types out of service outputs.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSyncQuarantineJSON_ServiceOwnedTypes(t *testing.T) {
	svc, st, _ := newTestSyncService(t)
	seedQuarantineAck(t, st, quarantineAckEligibilityCases()[0])
	result, err := svc.AcknowledgeQuarantine(t.Context(), quarantineAckEventID, "op-first", "reviewed")
	require.NoError(t, err)
	rows, err := svc.ListQuarantined(t.Context())
	require.NoError(t, err)
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"acknowledgement", result}, {"list", rows},
	} {
		t.Run(tc.name, func(t *testing.T) { requireServiceOutputType(t, reflect.TypeOf(tc.value)) })
	}
}

func requireServiceOutputType(t *testing.T, typ reflect.Type) {
	t.Helper()
	require.False(t, strings.Contains(typ.PkgPath(), "/internal/store/"), "output exposes storage type %s", typ)
	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice:
		requireServiceOutputType(t, typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			requireServiceOutputType(t, typ.Field(i).Type)
		}
	}
}

func TestSyncQuarantineJSON_Golden(t *testing.T) {
	for _, tc := range []struct {
		name, source string
		calls        int
		list, empty  bool
	}{
		{"empty_list", "pull", 0, true, true},
		{"unacknowledged_list", "pull", 0, true, false},
		{"first_acknowledgement", "pull", 1, false, false},
		{"acknowledged_list", "pull", 1, true, false},
		{"repeat_acknowledgement", "pull", 2, false, false},
		{"repeat_list", "pull", 2, true, false},
		{"sweep_first_acknowledgement", "sweep", 1, false, false},
		{"sweep_acknowledged_list", "sweep", 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, st, _ := newTestSyncService(t)
			if !tc.empty {
				fixture := quarantineAckEligibilityCases()[0]
				fixture.source = tc.source
				seedQuarantineAck(t, st, fixture)
			}
			var output any
			for n := 0; n < tc.calls; n++ {
				actor, reason := "op-first", "reviewed <retained>\n✓"
				if n > 0 {
					actor, reason = "op-second", "new reason is not recorded"
				}
				result, err := svc.AcknowledgeQuarantine(t.Context(), quarantineAckEventID, actor, reason)
				require.NoError(t, err)
				output = result
			}
			if tc.list {
				rows, err := svc.ListQuarantined(t.Context())
				require.NoError(t, err)
				output = rows
			}
			requireQuarantineGolden(t, tc.name, output, tc.list)
		})
	}
}

func requireQuarantineGolden(t *testing.T, name string, output any, indent bool) {
	t.Helper()
	var got []byte
	var err error
	if indent {
		got, err = json.MarshalIndent(output, "", "  ")
	} else {
		got, err = json.Marshal(output)
	}
	require.NoError(t, err)
	want, err := os.ReadFile(filepath.Join("testdata", "quarantine_json", name+".json"))
	require.NoError(t, err)
	require.Equal(t, string(want), string(got)+"\n")
}
