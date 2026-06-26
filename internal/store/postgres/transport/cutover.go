// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"

	syncpkg "github.com/hyper-swe/mtix/internal/sync"
)

// ProjectUIDCutoverReady reports whether a project may switch to
// uid-AUTHORITATIVE event keying (ADR-003 §7 Phase 3 / §10).
//
// It is the single decision point for the Phase-3 cutover: the answer is
// true only when EVERY active client on the project is at or above
// sync.UIDKeyedMinVersion — i.e. it delegates straight to the existing
// version gate, ProjectAllClientsAtLeast, with that minimum. Until then
// the project stays on the dual-carry regime: emitters still write BOTH
// node_id and uid, and apply PREFERS uid but falls back to node_id, so a
// pre-30.6 CLI keeps working on node_id (ADR-003 §7 Phase 3).
//
// Crucially this gate only ever DEFERS the cutover; it never forces it.
// Per ADR-003 §9 / docs/SECURITY-MODEL.md the gate is a liveness
// mechanism, not a security boundary: a closed result merely holds the
// project on the (already-correct) fallback keying; it cannot lose or
// corrupt a node. A nil-pool or query error returns (false, err) so a
// caller bug is never silently read as "ready".
func (p *Pool) ProjectUIDCutoverReady(ctx context.Context, projectPrefix string) (bool, error) {
	return p.ProjectAllClientsAtLeast(ctx, projectPrefix, syncpkg.UIDKeyedMinVersion)
}
