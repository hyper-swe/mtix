// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// resetPullCursorSQL rewinds both halves of the pull cursor to the start of
// the hub log, in one statement (MTIX-95.4; ADR-006 D6): it sets
// meta.sync.last_pulled_clock to 0 and empties meta.sync.last_pulled_event_id.
// DiscardLocal runs it with its other resets, so the next pull reads the
// whole hub log again. It is a constant with no parameters.
const resetPullCursorSQL = `UPDATE meta
	SET value = CASE key WHEN 'meta.sync.last_pulled_clock' THEN '0' ELSE '' END
	WHERE key IN ('meta.sync.last_pulled_clock', 'meta.sync.last_pulled_event_id')`
