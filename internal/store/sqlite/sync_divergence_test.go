// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// MTIX-15.6.1 divergent-history detection tests.

func divergenceTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "div.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	raw, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return s, raw
}

// canonicalEvent builds a known-shape SyncEvent for hash testing.
func canonicalEvent(t *testing.T) *model.SyncEvent {
	t.Helper()
	pl, err := model.EncodePayload(&model.CreateNodePayload{
		Title: "first node", Description: "d", NodeType: model.NodeTypeEpic,
	})
	require.NoError(t, err)
	return &model.SyncEvent{
		EventID:           "0193fa00-0000-7000-8000-000000000001",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpCreateNode,
		Payload:           pl,
		WallClockTS:       1700000000000,
		LamportClock:      1,
		VectorClock:       model.VectorClock{"alice": 1, "bob": 2},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
	}
}

// --- ComputeFirstEventHash ---

func TestComputeFirstEventHash_Deterministic(t *testing.T) {
	e := canonicalEvent(t)
	h1, err := ComputeFirstEventHash(e)
	require.NoError(t, err)
	h2, err := ComputeFirstEventHash(e)
	require.NoError(t, err)
	require.Equal(t, h1, h2)
	require.Len(t, h1, 64, "SHA-256 hex digest is 64 chars")
}

func TestComputeFirstEventHash_DeterministicAcrossVCMapOrder(t *testing.T) {
	// Two VCs with same logical content but different insertion order.
	// Go map iteration is randomized, so this would diverge if the
	// hash relied on map order.
	e1 := canonicalEvent(t)
	e1.VectorClock = model.VectorClock{}
	e1.VectorClock["alice"] = 1
	e1.VectorClock["bob"] = 2
	e1.VectorClock["carol"] = 3

	e2 := canonicalEvent(t)
	e2.VectorClock = model.VectorClock{}
	e2.VectorClock["carol"] = 3
	e2.VectorClock["bob"] = 2
	e2.VectorClock["alice"] = 1

	h1, err := ComputeFirstEventHash(e1)
	require.NoError(t, err)
	h2, err := ComputeFirstEventHash(e2)
	require.NoError(t, err)
	require.Equal(t, h1, h2,
		"VC insertion order MUST NOT affect the hash (uses sorted-key MarshalJSON)")
}

func TestComputeFirstEventHash_DeterministicAcrossPayloadKeyOrder(t *testing.T) {
	e1 := canonicalEvent(t)
	e1.Payload = json.RawMessage(`{"alpha":1,"beta":2,"gamma":3}`)

	e2 := canonicalEvent(t)
	e2.Payload = json.RawMessage(`{"gamma":3,"alpha":1,"beta":2}`)

	h1, err := ComputeFirstEventHash(e1)
	require.NoError(t, err)
	h2, err := ComputeFirstEventHash(e2)
	require.NoError(t, err)
	require.Equal(t, h1, h2,
		"payload key order MUST NOT affect the hash")
}

func TestComputeFirstEventHash_IgnoresWallClockTS(t *testing.T) {
	a := canonicalEvent(t)
	b := canonicalEvent(t)
	b.WallClockTS = 9999999999999
	ha, err := ComputeFirstEventHash(a)
	require.NoError(t, err)
	hb, err := ComputeFirstEventHash(b)
	require.NoError(t, err)
	require.Equal(t, ha, hb,
		"wall_clock_ts varies across replicas; MUST NOT contribute to hash")
}

func TestComputeFirstEventHash_IgnoresEventID(t *testing.T) {
	a := canonicalEvent(t)
	b := canonicalEvent(t)
	b.EventID = "0193fa00-0000-7000-8000-99999999999z"
	ha, err := ComputeFirstEventHash(a)
	require.NoError(t, err)
	hb, err := ComputeFirstEventHash(b)
	require.NoError(t, err)
	require.Equal(t, ha, hb,
		"event_id is per-emit unique; MUST NOT contribute to hash")
}

func TestComputeFirstEventHash_DiffersOnEachContentField(t *testing.T) {
	base := canonicalEvent(t)
	baseHash, err := ComputeFirstEventHash(base)
	require.NoError(t, err)

	mutations := []struct {
		name   string
		mutate func(*model.SyncEvent)
	}{
		{"project_prefix", func(e *model.SyncEvent) { e.ProjectPrefix = "OTHER" }},
		{"node_id", func(e *model.SyncEvent) { e.NodeID = "MTIX-2" }},
		{"op_type", func(e *model.SyncEvent) { e.OpType = model.OpUpdateField }},
		{"payload", func(e *model.SyncEvent) { e.Payload = json.RawMessage(`{"x":1}`) }},
		{"lamport_clock", func(e *model.SyncEvent) { e.LamportClock = 99 }},
		{"vector_clock", func(e *model.SyncEvent) { e.VectorClock = model.VectorClock{"alice": 5} }},
		{"author_id", func(e *model.SyncEvent) { e.AuthorID = "bob" }},
		{"author_machine_hash", func(e *model.SyncEvent) { e.AuthorMachineHash = "fedcba9876543210" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			e := canonicalEvent(t)
			tc.mutate(e)
			got, err := ComputeFirstEventHash(e)
			require.NoError(t, err)
			require.NotEqual(t, baseHash, got,
				"changing %s MUST change the hash", tc.name)
		})
	}
}

func TestComputeFirstEventHash_NilEvent(t *testing.T) {
	_, err := ComputeFirstEventHash(nil)
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}

func TestComputeFirstEventHash_NullPayloadAccepted(t *testing.T) {
	e := canonicalEvent(t)
	e.Payload = json.RawMessage("null")
	_, err := ComputeFirstEventHash(e)
	require.NoError(t, err, "null payload is canonical and hashable")
}

func TestComputeFirstEventHash_MalformedPayloadHashesVerbatim(t *testing.T) {
	// Hash function is robust: it returns a hash even for invalid JSON
	// payloads (the validator catches bad payloads elsewhere).
	e := canonicalEvent(t)
	e.Payload = json.RawMessage(`<<<not-json`)
	h, err := ComputeFirstEventHash(e)
	require.NoError(t, err)
	require.Len(t, h, 64)
}

// --- DetectDivergentHistory ---

func TestDetectDivergentHistory_HappyPathSameHash(t *testing.T) {
	require.NoError(t, DetectDivergentHistory("MTIX", "abc", "MTIX", "abc"))
}

func TestDetectDivergentHistory_DifferentPrefix(t *testing.T) {
	require.NoError(t, DetectDivergentHistory("MTIX", "abc", "OTHER", "xyz"),
		"different prefixes are not divergent — they are different projects")
}

func TestDetectDivergentHistory_HubFresh(t *testing.T) {
	require.NoError(t, DetectDivergentHistory("MTIX", "abc", "", ""),
		"hub has no record yet — local CLI is the first writer")
}

func TestDetectDivergentHistory_Mismatch(t *testing.T) {
	err := DetectDivergentHistory("MTIX", "aaa111", "MTIX", "bbb222")
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrSyncDivergentHistory))
	// Error MUST surface the four-resolution-paths guide so users
	// know what to do next.
	require.Contains(t, err.Error(), "--discard-local")
	require.Contains(t, err.Error(), "--rename-to")
	require.Contains(t, err.Error(), "--import-as")
	require.Contains(t, err.Error(), "--dry-run")
	// Both short hashes appear so the user can see WHICH histories disagree.
	require.Contains(t, err.Error(), "aaa111")
	require.Contains(t, err.Error(), "bbb222")
}

func TestDetectDivergentHistory_EmptyLocalPrefix(t *testing.T) {
	err := DetectDivergentHistory("", "abc", "MTIX", "xyz")
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrInvalidInput))
}

// --- Schema additions ---

func TestSchema_FreshDBHasLocalSyncProjectsTable(t *testing.T) {
	_, raw := divergenceTestStore(t)
	rows, err := raw.Query(
		`SELECT name FROM pragma_table_info('sync_projects') ORDER BY cid`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var got []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		got = append(got, n)
	}
	require.Equal(t,
		[]string{"project_prefix", "first_event_hash", "created_at", "schema_version", "last_seen_cli_version"},
		got)
}

func TestSchema_FreshDBHasFirstEventHashSentinels(t *testing.T) {
	_, raw := divergenceTestStore(t)
	for _, key := range []string{"meta.sync.first_event_hash", "meta.sync.project_prefix"} {
		var v string
		require.NoError(t, raw.QueryRow(
			`SELECT value FROM meta WHERE key = ?`, key,
		).Scan(&v))
		require.Equal(t, "", v, "%s starts empty", key)
	}
}

// --- GetOrComputeLocalFirstEventHash ---

func TestGetOrComputeLocalFirstEventHash_NoEventsReturnsEmpty(t *testing.T) {
	s, _ := divergenceTestStore(t)
	prefix, hash, err := s.GetOrComputeLocalFirstEventHash(context.Background())
	require.NoError(t, err)
	require.Empty(t, prefix)
	require.Empty(t, hash)
}

func TestGetOrComputeLocalFirstEventHash_ComputesAndCaches(t *testing.T) {
	s, raw := divergenceTestStore(t)
	ctx := context.Background()

	// Emit one event so the local store has a first event.
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      "MTIX-1",
			ProjectCode: "MTIX",
			OpType:      model.OpCreateNode,
			Author:      "alice",
			Payload:     json.RawMessage(`{"title":"x"}`),
		})
	}))

	prefix, hash, err := s.GetOrComputeLocalFirstEventHash(ctx)
	require.NoError(t, err)
	require.Equal(t, "MTIX", prefix)
	require.Len(t, hash, 64)

	// Cache populated.
	var cachedPrefix, cachedHash string
	require.NoError(t, raw.QueryRow(
		`SELECT value FROM meta WHERE key = 'meta.sync.project_prefix'`,
	).Scan(&cachedPrefix))
	require.Equal(t, "MTIX", cachedPrefix)
	require.NoError(t, raw.QueryRow(
		`SELECT value FROM meta WHERE key = 'meta.sync.first_event_hash'`,
	).Scan(&cachedHash))
	require.Equal(t, hash, cachedHash)

	// sync_projects row inserted.
	var n int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM sync_projects WHERE project_prefix = 'MTIX'`,
	).Scan(&n))
	require.Equal(t, 1, n)
}

func TestGetOrComputeLocalFirstEventHash_SecondCallUsesCache(t *testing.T) {
	s, raw := divergenceTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      "MTIX-1",
			ProjectCode: "MTIX",
			OpType:      model.OpCreateNode,
			Author:      "alice",
			Payload:     json.RawMessage(`{"title":"x"}`),
		})
	}))

	prefix1, hash1, err := s.GetOrComputeLocalFirstEventHash(ctx)
	require.NoError(t, err)

	// Subsequent emits do NOT change the first_event_hash (the FIRST
	// event is locked in once cached).
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      "MTIX-2",
			ProjectCode: "MTIX",
			OpType:      model.OpCreateNode,
			Author:      "alice",
			Payload:     json.RawMessage(`{"title":"y"}`),
		})
	}))

	prefix2, hash2, err := s.GetOrComputeLocalFirstEventHash(ctx)
	require.NoError(t, err)
	require.Equal(t, prefix1, prefix2)
	require.Equal(t, hash1, hash2,
		"once cached, the first_event_hash MUST be sticky — even if newer events land")

	// sync_projects has exactly one row (cache hit, no double-insert).
	var n int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM sync_projects`,
	).Scan(&n))
	require.Equal(t, 1, n)
}

// Ensure detection works end-to-end against locally-computed hashes.
func TestDetectDivergentHistory_EndToEndAgainstLocalCache(t *testing.T) {
	s, _ := divergenceTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      "MTIX-1",
			ProjectCode: "MTIX",
			OpType:      model.OpCreateNode,
			Author:      "alice",
			Payload:     json.RawMessage(`{"title":"x"}`),
		})
	}))
	prefix, hash, err := s.GetOrComputeLocalFirstEventHash(ctx)
	require.NoError(t, err)

	// Hub has the same prefix and same hash — no divergence.
	require.NoError(t, DetectDivergentHistory(prefix, hash, prefix, hash))

	// Hub has the same prefix and a DIFFERENT hash — divergent.
	err = DetectDivergentHistory(prefix, hash, prefix, "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	require.Error(t, err)
	require.True(t, errors.Is(err, model.ErrSyncDivergentHistory))
}

// --- canonicalJSON helper ---

func TestCanonicalJSON_SortsObjectKeys(t *testing.T) {
	out, err := canonicalJSON(json.RawMessage(`{"z":1,"a":2,"m":3}`))
	require.NoError(t, err)
	require.Equal(t, `{"a":2,"m":3,"z":1}`, string(out))
}

func TestCanonicalJSON_ArraysPreserveOrder(t *testing.T) {
	out, err := canonicalJSON(json.RawMessage(`[3,1,2]`))
	require.NoError(t, err)
	require.Equal(t, `[3,1,2]`, string(out),
		"array element order is semantic; canonicalization preserves it")
}

func TestCanonicalJSON_EmptyAndNullPassThrough(t *testing.T) {
	out, err := canonicalJSON(json.RawMessage(``))
	require.NoError(t, err)
	require.Equal(t, "", string(out))

	out, err = canonicalJSON(json.RawMessage(`null`))
	require.NoError(t, err)
	require.Equal(t, "null", string(out))
}

func TestCanonicalJSON_MalformedPassesThroughVerbatim(t *testing.T) {
	out, err := canonicalJSON(json.RawMessage(`<<<not-json`))
	require.NoError(t, err)
	require.Equal(t, `<<<not-json`, string(out),
		"the validator handles malformed payloads; canonical hash is robust to them")
}

// nowFromMetaOrSystem is used internally; this is a smoke test that
// the helper produces a parseable RFC3339Nano-compatible timestamp.
func TestNowFromMetaOrSystem(t *testing.T) {
	s, _ := divergenceTestStore(t)
	got, err := nowFromMetaOrSystem(context.Background(), s)
	require.NoError(t, err)
	_, err = time.Parse(time.RFC3339Nano, got)
	require.NoError(t, err)
}

func TestNowFromMetaOrSystem_NilStoreErrors(t *testing.T) {
	_, err := nowFromMetaOrSystem(context.Background(), nil)
	require.Error(t, err)
}

func TestShortHash(t *testing.T) {
	require.Equal(t, "abc", shortHash("abc"))
	require.Equal(t, "abcdefghijkl", shortHash("abcdefghijklmnopqrstuvwxyz"))
}
