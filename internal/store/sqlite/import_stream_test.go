// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-2.3.1: import decodes the export by streaming from the reader via a
// json.Decoder instead of os.ReadFile + json.Unmarshal on the whole byte slice,
// so a large export file is not held as both raw bytes AND a parsed tree at
// peak. Behaviour (including trailing-data strictness) matches json.Unmarshal.

func TestDecodeExportData_ParityWithUnmarshal(t *testing.T) {
	s := newTestStore(t)
	raw, err := json.Marshal(makeTestExport(t, s))
	require.NoError(t, err)

	var viaUnmarshal sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &viaUnmarshal))

	viaStream, err := sqlite.DecodeExportData(bytes.NewReader(raw))
	require.NoError(t, err)
	assert.Equal(t, &viaUnmarshal, viaStream,
		"streaming decode must produce the same ExportData as json.Unmarshal")
}

func TestDecodeExportData_RejectsTrailingData(t *testing.T) {
	s := newTestStore(t)
	raw, err := json.Marshal(makeTestExport(t, s))
	require.NoError(t, err)
	withJunk := append(append([]byte{}, raw...), []byte("  {\"extra\":true}")...)

	_, err = sqlite.DecodeExportData(bytes.NewReader(withJunk))
	require.Error(t, err, "trailing content after the export object must be rejected (parity with json.Unmarshal)")
}

func TestDecodeExportData_AllowsTrailingWhitespace(t *testing.T) {
	s := newTestStore(t)
	raw, err := json.Marshal(makeTestExport(t, s))
	require.NoError(t, err)
	withWS := append(append([]byte{}, raw...), []byte("\n\n\t")...)

	_, err = sqlite.DecodeExportData(bytes.NewReader(withWS))
	require.NoError(t, err, "a trailing newline/whitespace must be tolerated")
}

func TestDecodeExportData_RejectsMalformedJSON(t *testing.T) {
	_, err := sqlite.DecodeExportData(strings.NewReader(`{"version":1,`))
	require.Error(t, err)
}

func TestDecodeExportData_RoundTripsThroughImport(t *testing.T) {
	src := newTestStore(t)
	dst := newTestStore(t)
	raw, err := json.Marshal(makeTestExport(t, src))
	require.NoError(t, err)

	decoded, err := sqlite.DecodeExportData(bytes.NewReader(raw))
	require.NoError(t, err)

	res, err := dst.Import(context.Background(), decoded, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	assert.Equal(t, 2, res.NodesCreated, "streamed decode feeds a normal import")
}
