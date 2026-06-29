// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/jackc/pgx/v5"
)

// RenumberRequired is the structured outcome of a push whose incoming
// create_node duplicates an already-registered (project_prefix,
// display_path) on the hub registry (ADR-003 §6).
//
// First-writer-wins: the already-registered create keeps the number; the
// incoming create is NOT inserted and is surfaced here so the claimer can
// retry the next free number under the parent. A renumber-required
// outcome NEVER means a lost node — the rejected node still exists in the
// pusher's canonical local store; only its display number must move
// (ADR-003 §9: liveness, not a security boundary).
//
// Distinct from ConflictDescriptor: that is a field-level LWW conflict
// between concurrent updates (SYNC-DESIGN §8); this is a node-number
// collision at create time.
type RenumberRequired struct {
	// EventID is the rejected incoming create_node event.
	EventID string `json:"event_id"`
	// ProjectPrefix and DisplayPath are the contested registry key — the
	// node number the incoming create tried to claim. DisplayPath mirrors
	// SyncEvent.NodeID (the dot-notation display path, SYNC-DESIGN §3.1).
	ProjectPrefix string `json:"project_prefix"`
	DisplayPath   string `json:"display_path"`
	// RegisteredEventID is the create_node event that already holds the
	// number (the first writer that won), whether it was committed in a
	// prior push or claimed earlier in THIS push batch.
	RegisteredEventID string `json:"registered_event_id,omitempty"`
}

// effectiveUID is the create_node's STABLE logical identity (ADR-003 §2):
// the node's uid, which IS the node's original create-event id. When an
// event carries no uid (an old, pre-30.6 CLI on the dual-carry transition,
// ADR-003 §7), it falls back to the event's own id — exactly the self-anchor
// every fresh create takes — so uid-less events degrade to the pre-30.15
// (event_id-keyed) behavior with no special case.
func effectiveUID(e *model.SyncEvent) string {
	if e.UID != "" {
		return e.UID
	}
	return e.EventID
}

// registeredCreate is the already-registered create_node for a number: its
// event_id (the first writer that won), its effective uid (ADR-003 §2), and the
// restore_epoch it was hub-stamped with at acceptance (ADR-003 §15). The epoch
// is the restore-collision discriminator: a held create stamped in an EARLIER
// epoch than the current one means the hold predates the most recent operator
// restore-bump (a cross-epoch re-grant => Option B); an equal stamp is a
// same-epoch race => ordinary renumber.
type registeredCreate struct {
	eventID string
	uid     string
	epoch   int64
}

// lookupRegisteredCreate returns the create_node already registered for
// (prefix, displayPath), or a zero registeredCreate when the number is free.
// It reads the DERIVED registry — the partial unique index over the
// append-only log (ADR-003 §6, §13) — so there is no separate authoritative
// table to consult. The registered row's effective uid (its stored uid, or
// its event_id when uid is NULL) is returned so the caller can decide
// SAME-logical-node (no-op) vs DISTINCT-node (renumber) per ADR-003 §6/§9.
// Parameterized SQL only; errors redact any DSN.
//
// excludeEventID lets an idempotent re-push of the SAME create event see
// the number as free relative to itself: re-pushing a create must be a
// no-op, never a spurious renumber (ADR-003 §6).
func lookupRegisteredCreate(ctx context.Context, tx pgx.Tx, prefix, displayPath, excludeEventID string) (registeredCreate, error) {
	var (
		registeredID    string
		registeredUID   *string
		registeredEpoch int64
	)
	err := tx.QueryRow(ctx, `
		SELECT event_id, uid, restore_epoch
		FROM sync_events
		WHERE project_prefix = $1
		  AND node_id = $2
		  AND op_type = 'create_node'
		  AND event_id <> $3
		LIMIT 1`,
		prefix, displayPath, excludeEventID,
	).Scan(&registeredID, &registeredUID, &registeredEpoch)
	switch err {
	case nil:
		uid := registeredID // fallback when the stored uid is NULL/empty
		if registeredUID != nil && *registeredUID != "" {
			uid = *registeredUID
		}
		return registeredCreate{eventID: registeredID, uid: uid, epoch: registeredEpoch}, nil
	case pgx.ErrNoRows:
		return registeredCreate{}, nil
	default:
		return registeredCreate{}, fmt.Errorf("lookup registry %s/%s: %s",
			prefix, displayPath, redact.DSN(err.Error()))
	}
}

// registryKey is the (project_prefix, display_path) tuple the registry is
// keyed on. Used to detect a second create for the same number WITHIN a
// single push batch, before any row is committed and thus before the
// partial unique index can see it.
type registryKey struct {
	prefix string
	path   string
}

// batchClaim records the create_node that claimed a number earlier in THIS
// push batch: the claimant event_id and its effective uid (ADR-003 §2). The
// uid lets a later same-uid create in the same batch be recognized as the
// SAME logical node (a no-op) rather than a renumber.
type batchClaim struct {
	eventID string
	uid     string
}

// keyOf returns the registry key for a create_node event.
func keyOf(e *model.SyncEvent) registryKey {
	return registryKey{prefix: e.ProjectPrefix, path: e.NodeID}
}

// registryOutcome decides what the push loop does with one create_node
// against the registry (ADR-003 §6/§9). Exactly one of its cases holds:
//   - free: number unclaimed → caller inserts and (for a create) claims it.
//   - noop: the number is held by the SAME logical node (same effective uid)
//     — e.g. a --force re-backfill re-mints a fresh event_id for an existing
//     node — so the caller skips the insert and records NOTHING. This is the
//     MTIX-30.15 false-collision fix.
//   - renumber: the number is held by a DIFFERENT logical node (distinct
//     uid) in the SAME epoch — an ordinary collision (ADR-003 §6) — so the
//     caller skips the insert and surfaces the descriptor for the claimer to
//     retry the next free number.
//   - restoreCollision: the number is held by a DIFFERENT logical node whose
//     epoch stamp is EARLIER than the current restore_epoch — a cross-epoch
//     re-grant (ADR-003 §6.1/§15, Option B). The caller blocks the incoming
//     create (records it in sync_node_collisions for admin resolution) instead
//     of renumbering it. Reachable ONLY inside a restore window (current epoch
//     advanced by the operator); same-epoch collisions never produce it.
type registryOutcome struct {
	noop             bool
	renumber         *RenumberRequired
	restoreCollision *RestoreCollision
}

// registryDecide decides the registry outcome for a single event during a
// push (ADR-003 §6/§9), keyed on the node's stable uid (ADR-003 §2).
//
// A non-create event, or a create whose number is free, returns the zero
// outcome (free): the caller inserts it. A create whose number is already
// held returns noop when the holder is the SAME logical node (same effective
// uid) or renumber when it is a DIFFERENT one. Idempotent re-push of the
// SAME event (same event_id) is excluded from the lookup, so it falls
// through as free and the INSERT's ON CONFLICT DO NOTHING makes it a no-op.
//
// batchClaims serializes creates for the same number within ONE push: the
// partial unique index only sees committed rows, so the first create in the
// batch claims the key in-memory; later creates resolve against that claim —
// same uid → noop, distinct uid → renumber (ADR-003 §6.1/F-1). It is mutated
// in place to record a new claim.
//
// currentEpoch is the hub's restore_epoch read once for this push tx (ADR-003
// §15). It gates the restore-collision discriminator: only a committed held
// create stamped in an EARLIER epoch yields Option B. An intra-batch collision
// is same-epoch by construction (both creates are being accepted right now), so
// it always renumbers — never Option B.
func registryDecide(
	ctx context.Context, tx pgx.Tx, e *model.SyncEvent,
	batchClaims map[registryKey]batchClaim, currentEpoch int64,
) (registryOutcome, error) {
	if e.OpType != model.OpCreateNode {
		return registryOutcome{}, nil
	}
	key := keyOf(e)
	incomingUID := effectiveUID(e)

	if claim, claimed := batchClaims[key]; claimed {
		// Intra-batch: same epoch by construction, so distinct uid renumbers.
		return decideAgainst(e, claim.eventID, claim.uid, incomingUID), nil
	}

	reg, err := lookupRegisteredCreate(ctx, tx, e.ProjectPrefix, e.NodeID, e.EventID)
	if err != nil {
		return registryOutcome{}, err
	}
	if reg.eventID != "" {
		return decideAgainstRegistered(e, reg, incomingUID, currentEpoch), nil
	}

	batchClaims[key] = batchClaim{eventID: e.EventID, uid: incomingUID}
	return registryOutcome{}, nil
}

// decideAgainst resolves an incoming create against another create CLAIMED
// EARLIER IN THE SAME PUSH BATCH: a matching effective uid is the SAME logical
// node (no-op), a differing one is an ordinary collision (renumber). Both
// creates are being accepted in the current epoch, so a restore collision is
// impossible here (ADR-003 §6/§9, §15).
func decideAgainst(e *model.SyncEvent, registeredID, registeredUID, incomingUID string) registryOutcome {
	if registeredUID == incomingUID {
		return registryOutcome{noop: true}
	}
	return registryOutcome{renumber: &RenumberRequired{
		EventID: e.EventID, ProjectPrefix: e.ProjectPrefix,
		DisplayPath: e.NodeID, RegisteredEventID: registeredID,
	}}
}

// decideAgainstRegistered resolves an incoming create against the COMMITTED
// create that already holds its number, applying the epoch-gated
// restore-collision discriminator (ADR-003 §6.1, Addendum A §15):
//
//   - same effective uid → SAME logical node → noop (MTIX-30.15).
//   - distinct uid, held stamped in an EARLIER epoch than currentEpoch →
//     RESTORE collision (Option B): the two creates straddle an operator
//     restore-bump (a cross-epoch re-grant), so the incoming create is BLOCKED
//     for admin resolution, never silently renumbered.
//   - distinct uid, held stamped in the SAME (current) epoch → ordinary
//     concurrent-create race → renumber (ADR-003 §6, MTIX-30.7). This is the
//     normal-race false-positive the rejected UID-age trigger could not avoid;
//     here it is eliminated by construction (§15).
//
// Because currentEpoch advances ONLY by the operator (MarkRestored), in normal
// operation every create is stamped the same epoch, held.epoch == currentEpoch,
// and Option B is unreachable — a client cannot manufacture it (§15).
func decideAgainstRegistered(
	e *model.SyncEvent, reg registeredCreate, incomingUID string, currentEpoch int64,
) registryOutcome {
	if reg.uid == incomingUID {
		return registryOutcome{noop: true}
	}
	if reg.epoch < currentEpoch {
		return registryOutcome{restoreCollision: &RestoreCollision{
			EventID: e.EventID, ProjectPrefix: e.ProjectPrefix, DisplayPath: e.NodeID,
			HeldEventID: reg.eventID, HeldEpoch: reg.epoch, DetectedEpoch: currentEpoch,
		}}
	}
	return registryOutcome{renumber: &RenumberRequired{
		EventID: e.EventID, ProjectPrefix: e.ProjectPrefix,
		DisplayPath: e.NodeID, RegisteredEventID: reg.eventID,
	}}
}
