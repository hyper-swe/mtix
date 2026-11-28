// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-2.3.2.1 round 2: a repeated top-level array key is decoded the way
// encoding/json decodes it (each new element onto the element at the same
// index, then the slice cut to the new length), so the staged digests and
// values equal the historical reader's. Written red-first.
package sqlite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// stagedElements reads one staged array back in file order; nil when the file
// had no such array (absent or null).
func stagedElements[T any](t *testing.T, st *StagedExport, kind stageKind) []T {
	t.Helper()
	if st.presence[kind] == arrayAbsent {
		return nil
	}
	out := []T{}
	rows, err := st.db.QueryContext(context.Background(), stagedRowsInFileOrderSQL, int(kind))
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	for rows.Next() {
		var body []byte
		require.NoError(t, rows.Scan(&body))
		var v T
		require.NoError(t, json.Unmarshal(body, &v))
		out = append(out, v)
	}
	require.NoError(t, rows.Err())
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}

// duplicateKeyVariants are files that spell a top-level array more than once.
// In each the earlier array carries fields the later array omits, which the
// historical reader keeps (it decodes onto the existing element).
func duplicateKeyVariants() map[string]string {
	node := func(id, extra string) string { return `{"id":"` + id + `"` + extra + `}` }
	return map[string]string{
		"nodes second shorter": `{"nodes":[` + node("x", `,"description":"d","uid_hex":"ff00"`) + `,` +
			node("z", `,"title":"keep"`) + `],"nodes":[` + node("y", "") + `]}`,
		"nodes second longer": `{"nodes":[` + node("a", `,"title":"t","priority":3`) + `],"nodes":[` +
			node("b", "") + `,` + node("c", `,"title":"new"`) + `]}`,
		"nodes mixed case key": `{"nodes":[` + node("x", `,"description":"d"`) + `],"NODES":[` + node("y", "") + `]}`,
		"nodes embedded history": `{"nodes":[{"id":"x","history":[{"id":"h1","task_uid":"u","actor":"a","event_id_hex":"fe"}]}],` +
			`"nodes":[{"id":"y","history":[{"id":"h2"}]}]}`,
		"nodes plain uid over hex uid": `{"nodes":[{"id":"x","uid_hex":"ff00"}],"nodes":[{"id":"y","uid":"plain"}]}`,
		"nodes hex uid over plain uid": `{"nodes":[{"id":"x","uid":"plain"}],"nodes":[{"id":"y","uid_hex":"ff01"}]}`,
		"nodes second element null":    `{"nodes":[` + node("x", `,"description":"d"`) + `],"nodes":[null]}`,
		"nodes emptied then refilled":  `{"nodes":[` + node("x", `,"description":"d"`) + `],"nodes":[],"nodes":[` + node("y", "") + `]}`,
		"nodes shrink then grow": `{"nodes":[` + node("a", `,"title":"1"`) + `,` + node("b", `,"title":"2"`) + `,` +
			node("c", `,"title":"3"`) + `],"nodes":[` + node("d", "") + `],"nodes":[` + node("e", "") + `,` +
			node("f", "") + `,` + node("g", "") + `]}`,
		"nodes then null":          `{"nodes":[` + node("x", `,"description":"d"`) + `],"nodes":null}`,
		"nodes null then filled":   `{"nodes":null,"nodes":[` + node("y", "") + `]}`,
		"nodes three occurrences":  `{"nodes":[{"id":"a","title":"t","description":"d"}],"nodes":[{"id":"b","title":"t2"}],"nodes":[{"id":"c"}]}`,
		"dependencies second less": `{"dependencies":[{"from_id":"a","to_id":"b","dep_type":"blocks","created_at":"t"},{"from_id":"q","to_id":"r"}],"dependencies":[{"from_id":"c","to_id":"d"}]}`,
		"dependencies mixed case":  `{"dependencies":[{"from_id":"a","to_id":"b","dep_type":"blocks"}],"DEPENDENCIES":[{"from_id":"c"},{"to_id":"z"}]}`,
		"history second less": `{"history":[{"id":"h1","task_uid":"u","ts":"1","actor":"a","origin":"cli","kind":"k","field":"f","new":"n","event_id_hex":"ff"},` +
			`{"id":"h9","task_uid":"v"}],"history":[{"id":"h2"}]}`,
		"history mixed case": `{"history":[{"id":"h1","task_uid_hex":"75ff","actor":"a"}],"History":[{"id":"h2","ts":"2"},{"id":"h3","old":"o"}]}`,
		"every array twice": `{"nodes":[{"id":"x","title":"t"}],"dependencies":[{"from_id":"a","to_id":"b","dep_type":"d"}],` +
			`"history":[{"id":"h","task_uid":"u","actor":"a"}],"nodes":[{"id":"y"}],"dependencies":[{"from_id":"c"}],"history":[{"id":"h2"}]}`,
	}
}

func TestStageExport_RepeatedArrayKey_MatchesHistoricalReader(t *testing.T) {
	for name, text := range duplicateKeyVariants() {
		t.Run(name, func(t *testing.T) {
			raw := []byte(text)
			st, got := stagedDigests(t, raw)
			old, err := DecodeExportData(bytes.NewReader(raw))
			require.NoError(t, err)
			assert.Equal(t, historicalDigests(t, raw), got, "digests")
			assert.Equal(t, len(old.Nodes), st.NodeCount())
			assert.Equal(t, mustJSON(t, old.Nodes), mustJSON(t, stagedElements[exportNode](t, st, stageNodes)), "nodes")
			assert.Equal(t, mustJSON(t, old.Dependencies), mustJSON(t, stagedElements[exportDep](t, st, stageDeps)), "dependencies")
			assert.Equal(t, mustJSON(t, old.History), mustJSON(t, stagedElements[exportHistoryEntry](t, st, stageHistory)), "history")
		})
	}
}

// A file whose checksum was computed over the merged arrays verifies in both
// readers; one computed over the second array alone verifies in neither.
func TestStageExport_RepeatedArrayKey_VerifiesLikeHistoricalReader(t *testing.T) {
	arrays := `"nodes":[{"id":"x","description":"d","uid_hex":"ff00"}],"nodes":[{"id":"y"}]`
	verdicts := func(t *testing.T, sum, ids string) (oldOK, stagedOK bool) {
		t.Helper()
		raw := []byte(fmt.Sprintf(`{"node_count":1,"checksum":%q,"ids_checksum":%q,%s}`, sum, ids, arrays))
		old, err := DecodeExportData(bytes.NewReader(raw))
		require.NoError(t, err)
		oldOK, err = VerifyExportChecksum(old)
		require.NoError(t, err)
		st, _ := stagedDigests(t, raw)
		vErr := st.Verify(context.Background())
		if vErr != nil {
			require.ErrorIs(t, vErr, model.ErrInvalidInput)
		}
		return oldOK, vErr == nil
	}
	merged := historicalDigests(t, []byte(`{`+arrays+`}`))
	oldOK, stagedOK := verdicts(t, merged.Checksum, merged.IDs)
	assert.True(t, oldOK)
	assert.True(t, stagedOK)

	secondAlone := historicalDigests(t, []byte(`{"nodes":[{"id":"y"}]}`))
	require.NotEqual(t, merged.Checksum, secondAlone.Checksum, "the files must hash differently")
	oldOK, stagedOK = verdicts(t, secondAlone.Checksum, secondAlone.IDs)
	assert.False(t, oldOK)
	assert.False(t, stagedOK)
}

func TestStageExport_RepeatedArrayKey_ErrorsNameKindAndElement(t *testing.T) {
	st, err := StageExport(context.Background(),
		strings.NewReader(`{"nodes":[{"id":"x"},{"id":5}]}`), t.TempDir())
	require.Error(t, err)
	assert.Nil(t, st)
	assert.Contains(t, err.Error(), "nodes element 1")

	_, err = StageExport(context.Background(),
		strings.NewReader(`{"history":[{"id":"x","task_uid_hex":"zz"}]}`), t.TempDir())
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "history element 0")

	_, err = StageExport(context.Background(), strings.NewReader(`{"dependencies":"x"}`), t.TempDir())
	require.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "dependencies")
}
