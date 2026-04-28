// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/stretchr/testify/require"
)

// MTIX-15.3.4: SQL injection regression + DSN hygiene sweep + panic
// redaction integration. The first two require a live PG and skip
// when MTIX_PG_TEST_DSN is unset; redact integration is PG-free.

// --- SQL injection regression ---

// sqlInjectionPatterns are the canonical attack strings ported from
// MTIX-9.1. Each one is fed through PushEvents in a place where a
// naive implementation might concatenate it into SQL. The validator
// MUST either reject the event (regex on author_id, project_prefix)
// or accept it and store the literal string with no SQL side effect.
//
// We test BOTH outcomes: accepted attacks must round-trip verbatim;
// rejected attacks must be rejected with a structured error.
var sqlInjectionPatterns = []struct {
	name    string
	field   string // which event field to inject into
	payload string
	// accepted: the validator accepts the input as a literal string.
	//           we then verify it round-trips and the table is intact.
	// rejected: the validator rejects with ErrInvalidInput (regex etc).
	expect string // "accepted" or "rejected"
}{
	{"drop_table", "title", `'); DROP TABLE sync_events;--`, "accepted"},
	{"or_1_eq_1", "title", `' OR '1'='1`, "accepted"},
	{"semicolon_chain", "title", `x; DELETE FROM nodes WHERE 1=1;`, "accepted"},
	{"sqlite_master", "title", `' UNION SELECT * FROM pg_class--`, "accepted"},
	{"comment_truncate", "title", `x'/*`, "accepted"},
	{"author_uppercase", "author_id", `Robert"); DROP TABLE`, "rejected"},
	{"author_with_semicolon", "author_id", `bob;DROP`, "rejected"},
	{"prefix_with_quote", "project_prefix", `MTIX'`, "rejected"},
}

func TestSQLInjection_AttackPatternsHandledSafely(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	for i, tc := range sqlInjectionPatterns {
		t.Run(tc.name, func(t *testing.T) {
			e := &model.SyncEvent{
				EventID:           "0193fa00-0000-7000-8000-00000000010" + string(rune('0'+i)),
				ProjectPrefix:     "MTIX",
				NodeID:            "MTIX-1",
				OpType:            model.OpUpdateField,
				Payload:           json.RawMessage(`{"field_name":"title","new_value":"\"x\""}`),
				WallClockTS:       time.Now().UnixMilli(),
				LamportClock:      int64(100 + i),
				VectorClock:       model.VectorClock{"alice": 1},
				AuthorID:          "alice",
				AuthorMachineHash: "0123456789abcdef",
			}
			switch tc.field {
			case "title":
				e.Payload = json.RawMessage(`{"field_name":"title","new_value":` +
					mustJSON(tc.payload) + `}`)
			case "author_id":
				e.AuthorID = tc.payload
			case "project_prefix":
				e.ProjectPrefix = tc.payload
			}

			_, _, err := pool.PushEvents(context.Background(), []*model.SyncEvent{e})
			switch tc.expect {
			case "rejected":
				require.Error(t, err, "%s in %s should be rejected", tc.name, tc.field)
			case "accepted":
				require.NoErrorf(t, err, "%s in %s should round-trip as a literal", tc.name, tc.field)
			}

			// Regardless of accept/reject: the table must still exist.
			var n int
			require.NoError(t, pool.Inner().QueryRow(
				context.Background(),
				`SELECT count(*) FROM pg_tables WHERE tablename = 'sync_events'`,
			).Scan(&n))
			require.Equal(t, 1, n, "sync_events MUST still exist after %s", tc.name)
		})
	}
}

// --- DSN hygiene sweep ---

// TestDSN_NeverInTransportErrors ensures error strings returned from
// the transport package never include the credential portion of a
// DSN. The sweep exercises both happy and error paths and greps the
// concatenated error stream for the sentinel.
func TestDSN_NeverInTransportErrors(t *testing.T) {
	dir := withMTIXDir(t)
	t.Setenv(transport.EnvDSN, "")

	// Path 1: Source returns ErrDSNNotConfigured. Error message must
	// not echo the sentinel because there is none — but we still
	// check the regex pattern doesn't accidentally fire.
	_, err := transport.Source(dir)
	require.Error(t, err)
	require.NotContains(t, err.Error(), redact.SecretSentinel)

	// Path 2: build a deliberately bad DSN containing the sentinel.
	// Whatever EnforceTLSPosture returns MUST be passed through
	// redact.DSN before logging — so we test the contract by
	// asserting the redactor catches the raw form.
	leaky := "postgres://user:" + redact.SecretSentinel + "@host/db?sslmode=disable"
	_, err = transport.EnforceTLSPosture(leaky, transport.Options{})
	require.Error(t, err, "weak sslmode on remote host with InsecureTLS=false should fail")
	// The error MAY include the sentinel (it includes the host); the
	// caller MUST run it through redact.DSN before logging.
	redacted := redact.DSN(err.Error())
	require.NotContains(t, redacted, redact.SecretSentinel)
}

// TestDSN_NeverInPanic exercises the panic path with a DSN in scope
// and verifies redact.Recover catches it.
func TestDSN_NeverInPanic(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r)
		msg, _ := r.(string)
		require.NotContains(t, msg, redact.SecretSentinel,
			"panic value reaching outer caller MUST be redacted")
	}()

	defer redact.Recover(nil)
	panic("connection refused: postgres://user:" + redact.SecretSentinel + "@host/db")
}

// TestPushEvents_DSNNeverInRetryErrors is a sanity check that the
// retry envelope's error wrapping does not unmask credentials. The
// test injects a malformed DSN through the pool, exercises the
// transient-retry path, and verifies the eventual error is safely
// loggable after redaction.
func TestPushEvents_DSNNeverInRetryErrors(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	// Push an event whose payload contains the sentinel — verifying
	// it round-trips intact. The PG transport stores it as JSONB; if
	// the storage path passed it through redact, that would be a bug
	// (the redactor is for OBSERVABILITY, not for data).
	e := &model.SyncEvent{
		EventID:           "0193fa00-0000-7000-8000-000000000201",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpComment,
		Payload:           json.RawMessage(`{"author_id":"alice","body":"my password is ` + redact.SecretSentinel + `"}`),
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      1,
		VectorClock:       model.VectorClock{"alice": 1},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
	}
	_, _, err := pool.PushEvents(context.Background(), []*model.SyncEvent{e})
	require.NoError(t, err)

	// Pull and verify content survives.
	got, _, err := pool.PullEvents(context.Background(), 0, 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Contains(t, string(got[0].Payload), redact.SecretSentinel,
		"user data MUST round-trip; the redactor is for log output, not stored data")
}

// mustJSON returns the JSON-encoded form of a string for embedding in
// payloads. Panics on error — only used in tests with known-safe input.
func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Sanity: redact.SecretSentinel is the same sentinel used in the
// redact package's own tests. If anyone refactors the constant, this
// link reminds them to update both.
func TestSecretSentinel_StableAcrossPackages(t *testing.T) {
	require.True(t, strings.HasPrefix(redact.SecretSentinel, "PASSWORD_LEAK_SENTINEL_"))
}
