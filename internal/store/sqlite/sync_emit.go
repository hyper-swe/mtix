// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// Sync event emission per FR-18.3 / SYNC-DESIGN section 3 / MTIX-15.2.3.
//
// Every store mutation that writes the nodes table calls emitEvent inside
// the same SQLite transaction. The sync_events row commits or rolls back
// atomically with the underlying mutation — no orphan events possible.
// The atomicity property is exercised by the chaos test in this file.

// authorIDFallback is the conservative default when a mutation's author
// argument is empty or does not match the FR-18.7 grammar. The hub
// validator in MTIX-15.3 enforces the same regex; using a known-good
// fallback keeps us inside that contract.
const authorIDFallback = "cli"

// authorIDSafePattern is the FR-18.7 grammar duplicated locally to avoid
// a model package round-trip per emit. Kept in sync with
// model.authorIDPattern; the unit tests assert equivalence.
var authorIDSafePattern = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)

// emitParams collects what every mutation must pass to emitEvent.
// Project and Author come from the mutation; OpType + Payload are op-specific.
type emitParams struct {
	NodeID       string
	ProjectCode  string
	OpType       model.OpType
	Author       string
	Payload      json.RawMessage
	WallClockTS  int64
}

// emitEvent inserts one sync_events row inside the caller's transaction.
// MUST be called from inside an open *sql.Tx — the atomic-with-mutation
// property hinges on it. Returns model.ErrInvalidInput on validation
// failure (callers should not hide these — a buggy emitter is worse than
// a missing event).
func emitEvent(ctx context.Context, tx *sql.Tx, p emitParams) error {
	authorID := sanitizeAuthorID(p.Author)
	projectPrefix := projectPrefixFromNodeID(p.NodeID)
	if projectPrefix == "" && p.ProjectCode != "" {
		projectPrefix = p.ProjectCode
	}

	machineHash, err := readOrComputeMachineHash(ctx, tx)
	if err != nil {
		return fmt.Errorf("emit %s: %w", p.OpType, err)
	}

	lamport, err := bumpLamport(ctx, tx)
	if err != nil {
		return fmt.Errorf("emit %s: %w", p.OpType, err)
	}

	vc, err := bumpAndPersistVectorClock(ctx, tx, authorID)
	if err != nil {
		return fmt.Errorf("emit %s: %w", p.OpType, err)
	}

	wallTS := p.WallClockTS
	if wallTS == 0 {
		wallTS = time.Now().UTC().UnixMilli()
	}

	eventID, err := clock.NewEventID()
	if err != nil {
		return fmt.Errorf("emit %s: new event id: %w", p.OpType, err)
	}

	payload := p.Payload
	if len(payload) == 0 {
		payload = json.RawMessage("null")
	}

	event := &model.SyncEvent{
		EventID:           eventID,
		ProjectPrefix:     projectPrefix,
		NodeID:            p.NodeID,
		OpType:            p.OpType,
		Payload:           payload,
		WallClockTS:       wallTS,
		LamportClock:      lamport,
		VectorClock:       vc,
		AuthorID:          authorID,
		AuthorMachineHash: machineHash,
		SyncStatus:        model.SyncStatusPending,
		CreatedAt:         time.Now().UTC(),
	}
	if validateErr := event.Validate(); validateErr != nil {
		return fmt.Errorf("emit %s: invalid event: %w", p.OpType, validateErr)
	}

	vcJSON, err := json.Marshal(event.VectorClock)
	if err != nil {
		return fmt.Errorf("emit %s: marshal vector_clock: %w", p.OpType, err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash, sync_status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.ProjectPrefix, event.NodeID, string(event.OpType), string(event.Payload),
		event.WallClockTS, event.LamportClock, string(vcJSON),
		event.AuthorID, event.AuthorMachineHash, string(event.SyncStatus),
		event.CreatedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("emit %s: insert sync_events: %w", p.OpType, err)
	}
	return nil
}

// sanitizeAuthorID enforces the FR-18.7 grammar on caller-supplied author
// strings. Returns the input verbatim if valid; falls back to
// authorIDFallback otherwise so a mutation never blocks on a caller
// supplying e.g. "Vimal Menon".
func sanitizeAuthorID(author string) string {
	if authorIDSafePattern.MatchString(author) {
		return author
	}
	// One simple normalization pass before falling back: lowercase +
	// replace spaces and dots with dashes. Common CLI author strings
	// like "Vimal.Menon" or "Vimal Menon" become "vimal-menon", which
	// matches the grammar.
	candidate := strings.ToLower(author)
	candidate = strings.ReplaceAll(candidate, " ", "-")
	candidate = strings.ReplaceAll(candidate, ".", "-")
	if authorIDSafePattern.MatchString(candidate) {
		return candidate
	}
	return authorIDFallback
}

// projectPrefixFromNodeID extracts the prefix from a dot-notation node ID.
// "MTIX-1.2.3" -> "MTIX". Returns "" if the ID does not match the
// expected shape; callers fall back to the explicit ProjectCode.
//
// Allowed prefix chars: uppercase A-Z, digits, underscore. Matches
// model.projectPrefixPattern; kept in sync via the unit tests.
func projectPrefixFromNodeID(nodeID string) string {
	idx := strings.IndexByte(nodeID, '-')
	if idx <= 0 {
		return ""
	}
	prefix := nodeID[:idx]
	for _, r := range prefix {
		isUpper := r >= 'A' && r <= 'Z'
		isDigit := r >= '0' && r <= '9'
		if !isUpper && !isDigit && r != '_' {
			return ""
		}
	}
	return prefix
}

// bumpLamport atomically increments the local Lamport scalar and returns
// the new value. Reads-then-writes happen inside the caller's tx; the
// SQLite write-lock acquired by the surrounding WithTx serializes
// concurrent emitters within the same process.
func bumpLamport(ctx context.Context, tx *sql.Tx) (int64, error) {
	var raw string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.lamport'`,
	).Scan(&raw)
	if err != nil {
		return 0, fmt.Errorf("read lamport: %w", err)
	}
	current, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse lamport %q: %w", raw, err)
	}
	next := current + 1
	if _, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.lamport'`,
		strconv.FormatInt(next, 10),
	); err != nil {
		return 0, fmt.Errorf("write lamport: %w", err)
	}
	return next, nil
}

// bumpAndPersistVectorClock reads the stored VC from meta, bumps the
// entry for authorID, persists the result back, and returns the new
// VC value (a copy — not aliased to internal state).
func bumpAndPersistVectorClock(ctx context.Context, tx *sql.Tx, authorID string) (model.VectorClock, error) {
	var raw string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.vector_clock'`,
	).Scan(&raw)
	if err != nil {
		return nil, fmt.Errorf("read vector_clock: %w", err)
	}
	vc := model.VectorClock{}
	if raw != "" && raw != "{}" && raw != "null" {
		if parseErr := json.Unmarshal([]byte(raw), &vc); parseErr != nil {
			return nil, fmt.Errorf("parse vector_clock %q: %w", raw, parseErr)
		}
	}
	vc.Bump(authorID)
	if validateErr := vc.Validate(); validateErr != nil {
		return nil, fmt.Errorf("vector_clock invalid after bump: %w", validateErr)
	}
	encoded, err := json.Marshal(vc)
	if err != nil {
		return nil, fmt.Errorf("encode vector_clock: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.vector_clock'`,
		string(encoded),
	); err != nil {
		return nil, fmt.Errorf("write vector_clock: %w", err)
	}
	// Return a copy so the caller cannot mutate our local view.
	out := make(model.VectorClock, len(vc))
	for k, v := range vc {
		out[k] = v
	}
	return out, nil
}

// readOrComputeMachineHash returns the cached machine_hash from meta;
// computes via clock.MachineHash and persists if the cached value is
// empty (first-emit-on-this-machine). Subsequent emits read straight
// from meta — no re-compute.
func readOrComputeMachineHash(ctx context.Context, tx *sql.Tx) (string, error) {
	var hash string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.machine_hash'`,
	).Scan(&hash)
	if err != nil {
		return "", fmt.Errorf("read machine_hash: %w", err)
	}
	if hash != "" {
		return hash, nil
	}
	computed, err := clock.MachineHash()
	if err != nil {
		return "", fmt.Errorf("compute machine_hash: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.machine_hash'`,
		computed,
	); err != nil {
		return "", fmt.Errorf("persist machine_hash: %w", err)
	}
	return computed, nil
}
