// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSchema100Form_InvalidUTF8UID_BytesAndChecksumMatchPlainEncoding pins
// byte parity with mtix 0.5.3 (MTIX-187.7.15.1): it wrote the node as a plain
// struct, so encoding/json spelled an invalid-UTF-8 uid as � escapes. The
// 1.0.0 form must encode and hash exactly that, with no hex field.
func TestSchema100Form_InvalidUTF8UID_BytesAndChecksumMatchPlainEncoding(t *testing.T) {
	for _, uid := range []string{"evt\x80\x81", "evt\xe2\x82"} {
		node := exportNode{
			ID: "MTIX-1", Project: "MTIX", Title: "t", Status: "open", NodeType: "epic",
			CreatedAt: "2026-10-28T10:00:00Z", UpdatedAt: "2026-10-28T10:00:00Z", UID: uid,
		}
		form, err := Schema100Form(&ExportData{Nodes: []exportNode{node}})
		require.NoError(t, err)

		// What 0.5.3 wrote: the plain struct, no custom MarshalJSON.
		plain := exportNodePlain(node)
		wantNode, err := json.Marshal(plain)
		require.NoError(t, err)
		gotNode, err := json.Marshal(form.Nodes[0])
		require.NoError(t, err)
		require.Equal(t, string(wantNode), string(gotNode), "node bytes")
		require.NotContains(t, string(gotNode), "uid_hex")

		doc, err := json.Marshal(struct {
			Nodes []exportNodePlain `json:"nodes"`
			Deps  []exportDep       `json:"deps"`
		}{Nodes: []exportNodePlain{plain}})
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(doc)), form.Checksum, "1.0.0 checksum")
	}
}
