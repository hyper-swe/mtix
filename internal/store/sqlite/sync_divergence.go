// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-15.6.1 divergent-history detection per FR-18.13 / SYNC-DESIGN
// section 10. The detection compares a deterministic hash of the
// project's first event between the local CLI and the hub. A mismatch
// means two CLIs created the same prefix independently and need a
// reconciliation path (--discard-local, --rename-to, --import-as).

// DivergentHistoryGuide is appended to ErrSyncDivergentHistory
// errors so the user immediately sees the four resolution paths.
const DivergentHistoryGuide = "this prefix has divergent history vs the hub. " +
	"Choose one: " +
	"--discard-local (drop local, take hub state); " +
	"--rename-to NEWPREFIX (rewrite local IDs); " +
	"--import-as PARENT-ID (re-parent local tree); " +
	"--dry-run (preview only)"

// ComputeFirstEventHash returns a deterministic SHA-256 hex digest of
// the canonical content of e. The hash MUST agree across replicas for
// the same logical event so DetectDivergentHistory can compare local
// and hub views.
//
// Excluded fields:
//   - event_id: locally generated UUID v7; varies per emit.
//   - wall_clock_ts: laptop wall clock; varies across replicas.
//   - sync_status: local-mirror state; not part of event identity.
//   - created_at, retained_until: storage-time bookkeeping.
//
// Included fields are wrapped in a sorted-key JSON envelope so the
// resulting string is byte-identical regardless of map iteration
// order.
func ComputeFirstEventHash(e *model.SyncEvent) (string, error) {
	if e == nil {
		return "", fmt.Errorf("hash first event: nil: %w", model.ErrInvalidInput)
	}
	canonicalPayload, err := canonicalJSON(e.Payload)
	if err != nil {
		return "", fmt.Errorf("hash first event: payload: %w", err)
	}
	canonicalVC, err := json.Marshal(e.VectorClock)
	if err != nil {
		return "", fmt.Errorf("hash first event: vector_clock: %w", err)
	}

	// Envelope uses string-typed fields throughout so the payload —
	// which may be opaque bytes if the caller supplied malformed JSON —
	// never breaks the outer Marshal step. The hash treats the payload
	// as bytes regardless of structure; the validator catches malformed
	// payloads at a higher layer.
	envelope := map[string]string{
		"author_id":           e.AuthorID,
		"author_machine_hash": e.AuthorMachineHash,
		"lamport_clock":       fmt.Sprintf("%d", e.LamportClock),
		"node_id":             e.NodeID,
		"op_type":             string(e.OpType),
		"payload":             string(canonicalPayload),
		"project_prefix":      e.ProjectPrefix,
		"vector_clock":        string(canonicalVC),
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("hash first event: envelope: %w", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalJSON re-marshals raw via map[string]any so object keys end
// up sorted (Go's json.Marshal sorts map keys alphabetically). Arrays
// retain order. Scalars pass through unchanged.
//
// Returns the original raw verbatim if it is empty / null / a primitive
// — those are already canonical.
func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		// Not valid JSON at all — return verbatim. The validator
		// would catch this elsewhere; a hash function should not
		// reject inputs.
		return raw, nil
	}
	canonical, err := json.Marshal(generic)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

// DetectDivergentHistory compares the local first_event_hash against
// the hub's value for the same prefix. Returns nil when:
//   - prefixes differ (different projects);
//   - hub has no record for this prefix yet (hubHash == "");
//   - hashes match (same project, same history).
//
// Returns a wrapped model.ErrSyncDivergentHistory with the
// four-resolution-paths guide when the hashes mismatch.
func DetectDivergentHistory(localPrefix, localHash, hubPrefix, hubHash string) error {
	if localPrefix == "" {
		return fmt.Errorf("local prefix empty: %w", model.ErrInvalidInput)
	}
	if hubPrefix == "" || hubHash == "" {
		return nil // hub is fresh for this project
	}
	if localPrefix != hubPrefix {
		return nil // different projects, not a divergence
	}
	if localHash == hubHash {
		return nil
	}
	return fmt.Errorf("prefix %q: local=%s hub=%s: %s: %w",
		localPrefix, shortHash(localHash), shortHash(hubHash),
		DivergentHistoryGuide, model.ErrSyncDivergentHistory)
}

// shortHash returns the first 12 chars of a hex digest for safe
// inclusion in error messages.
func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

// GetOrComputeLocalFirstEventHash returns the cached first_event_hash
// for the local project. On first call after a fresh DB it scans the
// sync_events table for the lowest-lamport event, computes the hash,
// caches it in meta.sync.first_event_hash, and writes a row to
// sync_projects.
//
// Returns ("", "", nil) when there are no events yet (the local CLI
// hasn't emitted anything; divergence detection is irrelevant).
func (s *Store) GetOrComputeLocalFirstEventHash(ctx context.Context) (prefix, hash string, err error) {
	cachedPrefix, cachedHash, ok, err := s.readCachedFirstEventHash(ctx)
	if err != nil {
		return "", "", err
	}
	if ok {
		return cachedPrefix, cachedHash, nil
	}
	return s.computeAndCacheFirstEventHash(ctx)
}

// readCachedFirstEventHash reads meta.sync.project_prefix and
// meta.sync.first_event_hash. Returns (prefix, hash, true, nil) when
// both are non-empty; (.., .., false, nil) when uncached; error on
// SQL failure.
func (s *Store) readCachedFirstEventHash(ctx context.Context) (string, string, bool, error) {
	var prefix, hash string
	err := s.readDB.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.project_prefix'`,
	).Scan(&prefix)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", false, fmt.Errorf("read cached prefix: %w", err)
	}
	err = s.readDB.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.first_event_hash'`,
	).Scan(&hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", "", false, fmt.Errorf("read cached hash: %w", err)
	}
	if prefix == "" || hash == "" {
		return "", "", false, nil
	}
	return prefix, hash, true, nil
}

// computeAndCacheFirstEventHash scans sync_events for the lowest-
// lamport event, computes the hash, persists it, and creates the
// sync_projects row. Returns ("", "", nil) when there are no events.
func (s *Store) computeAndCacheFirstEventHash(ctx context.Context) (string, string, error) {
	event, err := s.scanFirstEvent(ctx)
	if err != nil {
		return "", "", err
	}
	if event == nil {
		return "", "", nil
	}

	hash, err := ComputeFirstEventHash(event)
	if err != nil {
		return "", "", fmt.Errorf("compute first hash: %w", err)
	}
	now, err := nowFromMetaOrSystem(ctx, s)
	if err != nil {
		return "", "", err
	}

	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		if _, txErr := tx.ExecContext(ctx,
			`UPDATE meta SET value = ? WHERE key = 'meta.sync.project_prefix'`,
			event.ProjectPrefix,
		); txErr != nil {
			return fmt.Errorf("write cached prefix: %w", txErr)
		}
		if _, txErr := tx.ExecContext(ctx,
			`UPDATE meta SET value = ? WHERE key = 'meta.sync.first_event_hash'`,
			hash,
		); txErr != nil {
			return fmt.Errorf("write cached hash: %w", txErr)
		}
		if _, txErr := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO sync_projects
			  (project_prefix, first_event_hash, created_at, schema_version)
			VALUES (?, ?, ?, 1)`,
			event.ProjectPrefix, hash, now,
		); txErr != nil {
			return fmt.Errorf("write sync_projects row: %w", txErr)
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	return event.ProjectPrefix, hash, nil
}

// scanFirstEvent returns the lowest-lamport sync_events row, or nil
// when the table is empty. Sort ties broken by event_id so two CLIs
// with simultaneous lowest-lamport emits agree on which is "first".
func (s *Store) scanFirstEvent(ctx context.Context) (*model.SyncEvent, error) {
	row := s.readDB.QueryRowContext(ctx, `
		SELECT event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		FROM sync_events
		ORDER BY lamport_clock ASC, event_id ASC
		LIMIT 1`)
	var e model.SyncEvent
	var opType, payload, vc string
	err := row.Scan(
		&e.EventID, &e.ProjectPrefix, &e.NodeID, &opType, &payload,
		&e.WallClockTS, &e.LamportClock, &vc,
		&e.AuthorID, &e.AuthorMachineHash,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scan first event: %w", err)
	}
	e.OpType = model.OpType(opType)
	e.Payload = json.RawMessage(payload)
	if err := json.Unmarshal([]byte(vc), &e.VectorClock); err != nil {
		return nil, fmt.Errorf("decode VC: %w", err)
	}
	return &e, nil
}

// nowFromMetaOrSystem returns the current time in RFC3339Nano format.
// Wrapped to allow tests to inject deterministic timestamps via the
// store's existing clock seam (s.clock); in production this delegates
// to system time.
func nowFromMetaOrSystem(_ context.Context, s *Store) (string, error) {
	if s == nil || s.clock == nil {
		return "", fmt.Errorf("nil store/clock")
	}
	return s.clock().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), nil
}

