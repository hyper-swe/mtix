// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

type contactPublicRow struct {
	ID     string                     `json:"id"`
	Status string                     `json:"status"`
	Detail string                     `json:"detail"`
	Fix    string                     `json:"fix"`
	Data   map[string]json.RawMessage `json:"data"`
}

func publicContactRow(t *testing.T, body string) contactPublicRow {
	t.Helper()
	var rows []contactPublicRow
	require.NoError(t, json.Unmarshal([]byte(body), &rows), body)
	for _, row := range rows {
		if row.ID == "sync.hub-contact" {
			return row
		}
	}
	t.Fatal("sync.hub-contact missing from actual CLI JSON")
	return contactPublicRow{}
}

func seedPublicContactFailure(t *testing.T, root string) *hubContactSource {
	t.Helper()
	src, err := newHubContactSource(filepath.Join(root, ".mtix"))
	require.NoError(t, err)
	require.NoError(t, src.state.RecordHubContact(src.key(), oplocal.HubContact{
		At: time.Now(), Route: "poll", OK: false, ErrorClass: "unreachable"}))
	return src
}

func TestDoctorCLI_ManualPullFix_ReachesHubAndClearsOutcome(t *testing.T) {
	_ = openCmdHub(t)
	dsn := os.Getenv(transport.EnvDSN)
	root := doctorCLIBoard(t, "")
	src := seedPublicContactFailure(t, root)
	cfg, err := service.NewConfigService(src.configPath())
	require.NoError(t, err)
	_, err = cfg.Set("sync.hub_contact", "activity")
	require.NoError(t, err)
	before := publicContactRow(t, doctorCLIExecute(t, root, dsn, "doctor", "--json").stdout)
	require.Equal(t, "fail", before.Status)
	i := strings.LastIndex(before.Fix, "mtix sync pull")
	require.NotEqual(t, -1, i, "the row must supply the actual recovery command")
	command := strings.Fields(before.Fix[i:])
	// The owned PG fixture has no TLS; retain its explicit loopback-only development option.
	pull := doctorCLIExecute(t, root, dsn, append(command[1:], "--insecure-tls")...)
	require.Zero(t, pull.exit, pull.stderr)
	after := publicContactRow(t, doctorCLIExecute(t, root, dsn, "doctor", "--json").stdout)
	require.NotEqual(t, "fail", after.Status, after.Detail)
	require.Contains(t, after.Detail, "last pull succeeded")
	rec, present, err := src.state.ReadHubContact(src.key())
	require.NoError(t, err)
	require.True(t, present)
	require.True(t, rec.OK)
	require.Equal(t, "pull", rec.Route)
}

func TestDoctorCLI_LastContact_PublicStructuredJSON(t *testing.T) {
	root := doctorCLIBoard(t, "")
	seedPublicContactFailure(t, root)
	out := doctorCLIExecute(t, root, "postgres://user:public-json-secret@/%zz", "doctor", "--json")
	row := publicContactRow(t, out.stdout)
	require.Contains(t, row.Data, "last_contact")
	var contact map[string]any
	require.NoError(t, json.Unmarshal(row.Data["last_contact"], &contact))
	require.Equal(t, "unreachable", contact["error_class"])
	require.Equal(t, false, contact["ok"])
	require.Equal(t, "poll", contact["route"])
	require.NotEmpty(t, contact["at"])
	require.NotContains(t, out.stdout+out.stderr, "public-json-secret")
}
